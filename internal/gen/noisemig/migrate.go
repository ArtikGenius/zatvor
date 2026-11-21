// Package noisemig translates legacy freedom "noises" reached through
// sockopt.dialerProxy into a streamSettings.finalmask.udp noise item
// (docs/research/xray-core.md §3, spec §6.4). It is off by default: callers
// switch it on only for an Xray release that no longer accepts "noises".
package noisemig

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"unicode"
)

// Reasons a migration is refused. When any outbound is refused the whole
// config is left as it is (legacy keeps running, the caller warns).
const (
	ReasonNoiseInFinalmask        = "finalmask.udp already has a noise item"
	ReasonChainedDialer           = "noise freedom has its own dialerProxy"
	ReasonApplyTo                 = "noise uses applyTo, which finalmask lacks"
	ReasonRedirect53              = "noise freedom redirects to port 53"
	ReasonFreedomHasOtherSettings = "noise freedom has settings besides noises"
)

// Refusal names an outbound whose legacy noise was not migrated, and why.
type Refusal struct{ Tag, Reason string }

// Report lists the outbound tags that got a finalmask noise item, or the
// refusals that kept the config unchanged.
type Report struct {
	Migrated []string
	Refused  []Refusal
}

// candidate is an outbound X dialing through a freedom outbound D with noises.
type candidate struct {
	x, d   *object
	xTag   string
	dTag   string
	noises []any
}

// Migrate returns the translated config. When there is nothing to change, a
// migration is refused, or an error occurs, it returns cfg itself unchanged.
func Migrate(cfg []byte) ([]byte, Report, error) {
	root, err := parseOrdered(cfg)
	if err != nil {
		return cfg, Report{}, fmt.Errorf("noisemig: %w", err)
	}
	rootObj, ok := root.(*object)
	if !ok {
		return cfg, Report{}, nil
	}
	outbounds := arrayAt(rootObj, "outbounds")

	byTag := map[string]*object{}
	for _, ob := range outbounds {
		if o, ok := ob.(*object); ok {
			if tag, ok := stringAt(o, "tag"); ok {
				byTag[tag] = o
			}
		}
	}

	var cands []candidate
	for _, ob := range outbounds {
		x, ok := ob.(*object)
		if !ok {
			continue
		}
		dTag, ok := stringAt(x, "streamSettings", "sockopt", "dialerProxy")
		if !ok || dTag == "" {
			continue
		}
		d := byTag[dTag]
		if d == nil {
			continue
		}
		if p, _ := stringAt(d, "protocol"); p != "freedom" {
			continue
		}
		noises := arrayAt(d, "settings", "noises")
		if len(noises) == 0 {
			continue
		}
		xTag, _ := stringAt(x, "tag")
		cands = append(cands, candidate{x: x, d: d, xTag: xTag, dTag: dTag, noises: noises})
	}

	var rep Report
	translated := make([][]any, len(cands))
	for i, c := range cands {
		if reason := refusal(c); reason != "" {
			rep.Refused = append(rep.Refused, Refusal{Tag: c.xTag, Reason: reason})
			continue
		}
		items, err := translateNoises(c.noises)
		if err != nil {
			return cfg, Report{}, fmt.Errorf("noisemig: outbound %q via %q: %w", c.xTag, c.dTag, err)
		}
		translated[i] = items
	}
	if len(rep.Refused) > 0 {
		return cfg, rep, nil
	}

	changed := false
	var freedoms []candidate // distinct D, in outbound order
	seen := map[*object]bool{}
	for i, c := range cands {
		appendNoise(c.x, translated[i])
		ss := objectAt(c.x, "streamSettings")
		sockopt := objectAt(ss, "sockopt")
		sockopt.del("dialerProxy")
		if len(sockopt.members) == 0 {
			ss.del("sockopt")
		}
		if !seen[c.d] {
			seen[c.d] = true
			freedoms = append(freedoms, c)
		}
		rep.Migrated = append(rep.Migrated, c.xTag)
		changed = true
	}

	for _, f := range freedoms {
		if referenced(rootObj, f.d, f.dTag) {
			// Still routed to: keep it, minus the noises now in finalmask.
			objectAt(f.d, "settings").del("noises")
			continue
		}
		removeOutbound(rootObj, f.d)
	}

	// Drop empty "noises": [] from the remaining freedom outbounds; each one
	// triggers Xray's deprecation warning.
	for _, ob := range arrayAt(rootObj, "outbounds") {
		o, ok := ob.(*object)
		if !ok {
			continue
		}
		if p, _ := stringAt(o, "protocol"); p != "freedom" {
			continue
		}
		settings := objectAt(o, "settings")
		if v, ok := settings.get("noises"); ok {
			if arr, ok := v.([]any); ok && len(arr) == 0 {
				settings.del("noises")
				changed = true
			}
		}
	}

	if !changed {
		return cfg, Report{}, nil
	}
	out, err := encodeOrdered(rootObj)
	if err != nil {
		return cfg, Report{}, fmt.Errorf("noisemig: %w", err)
	}
	return out, rep, nil
}

// refusal returns the first reason the candidate cannot be translated
// mechanically, or "".
func refusal(c candidate) string {
	for _, m := range arrayAt(c.x, "streamSettings", "finalmask", "udp") {
		if o, ok := m.(*object); ok {
			if t, _ := stringAt(o, "type"); strings.EqualFold(t, "noise") {
				return ReasonNoiseInFinalmask
			}
		}
	}
	if dp, ok := c.d.get("streamSettings"); ok {
		if v, ok := stringAt(asObject(dp), "sockopt", "dialerProxy"); ok && v != "" {
			return ReasonChainedDialer
		}
	}
	if p, ok := c.d.get("proxySettings"); ok && !isEmpty(p) {
		return ReasonChainedDialer
	}
	for _, n := range c.noises {
		if o, ok := n.(*object); ok {
			if v, ok := o.get("applyTo"); ok && !isEmpty(v) {
				return ReasonApplyTo
			}
		}
	}
	if r, ok := stringAt(c.d, "settings", "redirect"); ok && r != "" {
		if _, port, err := net.SplitHostPort(r); err == nil && port == "53" {
			return ReasonRedirect53
		}
	}
	for _, m := range c.d.members {
		switch m.key {
		case "tag", "protocol":
		case "settings":
			for _, s := range asObject(m.val).members {
				if s.key != "noises" && !isEmpty(s.val) {
					return ReasonFreedomHasOtherSettings
				}
			}
		default:
			if !isEmpty(m.val) {
				return ReasonFreedomHasOtherSettings
			}
		}
	}
	return ""
}

// translateNoises maps legacy noise items to finalmask noise items (§3 table).
func translateNoises(noises []any) ([]any, error) {
	out := make([]any, 0, len(noises))
	for i, n := range noises {
		o, ok := n.(*object)
		if !ok {
			return nil, fmt.Errorf("noise %d is not an object", i)
		}
		for _, m := range o.members {
			switch m.key {
			case "type", "packet", "delay", "applyTo":
			default:
				return nil, fmt.Errorf("noise %d: unknown field %q", i, m.key)
			}
		}
		typ, _ := stringAt(o, "type")
		packet, _ := stringAt(o, "packet")
		item := &object{}
		switch typ {
		case "hex":
			p := strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, packet)
			if _, err := hex.DecodeString(p); err != nil {
				return nil, fmt.Errorf("noise %d: bad hex packet: %w", i, err)
			}
			item.set("type", "hex")
			item.set("packet", p)
		case "str":
			item.set("type", "str")
			item.set("packet", packet)
		case "base64":
			// Legacy accepts URL-safe and unpadded input; finalmask wants std padded.
			p := strings.TrimRight(strings.NewReplacer("-", "+", "_", "/").Replace(strings.TrimSpace(packet)), "=")
			raw, err := base64.RawStdEncoding.DecodeString(p)
			if err != nil {
				return nil, fmt.Errorf("noise %d: bad base64 packet: %w", i, err)
			}
			item.set("type", "base64")
			item.set("packet", base64.StdEncoding.EncodeToString(raw))
		case "rand":
			item.set("rand", packet)
		default:
			return nil, fmt.Errorf("noise %d: unknown type %q", i, typ)
		}
		if d, ok := o.get("delay"); ok {
			item.set("delay", d)
		}
		out = append(out, item)
	}
	return out, nil
}

// appendNoise adds the noise mask as the last finalmask.udp item (nearest the
// wire), creating streamSettings.finalmask.udp when missing.
func appendNoise(x *object, items []any) {
	ss := ensureObject(x, "streamSettings")
	fm := ensureObject(ss, "finalmask")
	udp, _ := fm.get("udp")
	arr, _ := udp.([]any)
	settings := &object{}
	settings.set("reset", json.Number("0"))
	settings.set("noise", items)
	mask := &object{}
	mask.set("type", "noise")
	mask.set("settings", settings)
	fm.set("udp", append(arr, mask))
}

// referenced reports whether anything but d's own "tag" names tag. Routing
// balancer and observatory selectors match by prefix.
func referenced(root, d *object, tag string) bool {
	var walk func(v any, key string, owner *object) bool
	walk = func(v any, key string, owner *object) bool {
		switch t := v.(type) {
		case *object:
			for _, m := range t.members {
				if walk(m.val, m.key, t) {
					return true
				}
			}
		case []any:
			for _, e := range t {
				if walk(e, key, owner) {
					return true
				}
			}
		case string:
			if owner == d && key == "tag" {
				return false
			}
			if t == tag {
				return true
			}
			if (key == "selector" || key == "subjectSelector") && t != "" && strings.HasPrefix(tag, t) {
				return true
			}
		}
		return false
	}
	return walk(root, "", nil)
}

func removeOutbound(root, d *object) {
	obs := arrayAt(root, "outbounds")
	kept := make([]any, 0, len(obs))
	for _, ob := range obs {
		if o, ok := ob.(*object); !ok || o != d {
			kept = append(kept, ob)
		}
	}
	root.set("outbounds", kept)
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case *object:
		return len(t.members) == 0
	case []any:
		return len(t) == 0
	}
	return false
}

func asObject(v any) *object {
	if o, ok := v.(*object); ok {
		return o
	}
	return &object{}
}

// objectAt follows keys through nested objects; a missing step yields an
// empty, detached object.
func objectAt(o *object, keys ...string) *object {
	for _, k := range keys {
		v, _ := o.get(k)
		o = asObject(v)
	}
	return o
}

func ensureObject(o *object, key string) *object {
	if v, ok := o.get(key); ok {
		if c, ok := v.(*object); ok {
			return c
		}
	}
	c := &object{}
	o.set(key, c)
	return c
}

func arrayAt(o *object, keys ...string) []any {
	v, _ := objectAt(o, keys[:len(keys)-1]...).get(keys[len(keys)-1])
	arr, _ := v.([]any)
	return arr
}

func stringAt(o *object, keys ...string) (string, bool) {
	v, _ := objectAt(o, keys[:len(keys)-1]...).get(keys[len(keys)-1])
	s, ok := v.(string)
	return s, ok
}
