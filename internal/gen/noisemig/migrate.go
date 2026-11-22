// Package noisemig translates legacy freedom "noises" reached through
// sockopt.dialerProxy into a streamSettings.finalmask.udp noise item
// (docs/research/xray-core.md §3, spec §6.4). It is off by default: callers
// switch it on only for an Xray release that no longer accepts "noises".
//
// For each outbound X whose streamSettings.sockopt.dialerProxy names a freedom
// outbound D with non-empty settings.noises, Migrate appends
// {"type":"noise","settings":{"reset":0,"noise":[...]}} as the last
// finalmask.udp item of X, removes the dialerProxy (and an emptied sockopt),
// removes D, and drops empty "noises" arrays from other freedom outbounds.
// It refuses, leaving the config byte-identical, when the translation would
// not be mechanical: noise already in finalmask, D chained to another proxy,
// applyTo, a port-53 redirect, D with other settings, or D referenced by
// anything besides the dialerProxy fields being migrated (a routing rule,
// proxySettings, a balancer or observatory selector), since removing or
// stripping D would change the traffic routed to it.
package noisemig

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
	"unicode"

	"github.com/ArtikGenius/zatvor/internal/ojson"
)

// Reasons a migration is refused. When any outbound is refused the whole
// config is left as it is (legacy keeps running, the caller warns).
const (
	ReasonNoiseInFinalmask        = "finalmask.udp already has a noise item"
	ReasonChainedDialer           = "noise freedom has its own dialerProxy"
	ReasonApplyTo                 = "noise uses applyTo, which finalmask lacks"
	ReasonRedirect53              = "noise freedom redirects to port 53"
	ReasonFreedomHasOtherSettings = "noise freedom has settings besides noises"
	ReasonFreedomReferenced       = "noise freedom is referenced by more than dialerProxy"
)

// randShape is the legacy rand packet: a length "n" or a range "a-b".
var randShape = regexp.MustCompile(`^[0-9]+(-[0-9]+)?$`)

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
	x, d   *ojson.Object
	xTag   string
	dTag   string
	noises []any
}

// Migrate returns the translated config. When there is nothing to change, a
// migration is refused, or an error occurs, it returns cfg itself unchanged.
func Migrate(cfg []byte) ([]byte, Report, error) {
	root, err := ojson.Parse(cfg)
	if err != nil {
		return cfg, Report{}, fmt.Errorf("noisemig: %w", err)
	}
	rootObj, ok := root.(*ojson.Object)
	if !ok {
		return cfg, Report{}, nil
	}
	outbounds := arrayAt(rootObj, "outbounds")

	byTag := map[string]*ojson.Object{}
	for _, ob := range outbounds {
		if o, ok := ob.(*ojson.Object); ok {
			if tag, ok := stringAt(o, "tag"); ok {
				byTag[tag] = o
			}
		}
	}

	var cands []candidate
	for _, ob := range outbounds {
		x, ok := ob.(*ojson.Object)
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

	// The dialerProxy fields about to be removed do not count as references.
	migrating := map[*ojson.Object]bool{}
	for _, c := range cands {
		migrating[objectAt(c.x, "streamSettings", "sockopt")] = true
	}

	var rep Report
	translated := make([][]any, len(cands))
	for i, c := range cands {
		reason := refusal(c)
		if reason == "" && referenced(rootObj, c.d, c.dTag, migrating) {
			reason = ReasonFreedomReferenced
		}
		if reason != "" {
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
	seen := map[*ojson.Object]bool{}
	for i, c := range cands {
		appendNoise(c.x, translated[i])
		ss := objectAt(c.x, "streamSettings")
		sockopt := objectAt(ss, "sockopt")
		sockopt.Delete("dialerProxy")
		if sockopt.Len() == 0 {
			ss.Delete("sockopt")
		}
		if !seen[c.d] {
			seen[c.d] = true
			freedoms = append(freedoms, c)
		}
		rep.Migrated = append(rep.Migrated, c.xTag)
		changed = true
	}

	for _, f := range freedoms {
		removeOutbound(rootObj, f.d)
	}

	// Drop empty "noises": [] from the remaining freedom outbounds; each one
	// triggers Xray's deprecation warning.
	for _, ob := range arrayAt(rootObj, "outbounds") {
		o, ok := ob.(*ojson.Object)
		if !ok {
			continue
		}
		if p, _ := stringAt(o, "protocol"); p != "freedom" {
			continue
		}
		settings := objectAt(o, "settings")
		if v, ok := settings.Get("noises"); ok {
			if arr, ok := v.([]any); ok && len(arr) == 0 {
				settings.Delete("noises")
				changed = true
			}
		}
	}

	if !changed {
		return cfg, Report{}, nil
	}
	out, err := ojson.Marshal(rootObj)
	if err != nil {
		return cfg, Report{}, fmt.Errorf("noisemig: %w", err)
	}
	return out, rep, nil
}

// refusal returns the first reason the candidate cannot be translated
// mechanically, or "".
func refusal(c candidate) string {
	for _, m := range arrayAt(c.x, "streamSettings", "finalmask", "udp") {
		if o, ok := m.(*ojson.Object); ok {
			if t, _ := stringAt(o, "type"); strings.EqualFold(t, "noise") {
				return ReasonNoiseInFinalmask
			}
		}
	}
	if dp, ok := c.d.Get("streamSettings"); ok {
		if v, ok := stringAt(asObject(dp), "sockopt", "dialerProxy"); ok && v != "" {
			return ReasonChainedDialer
		}
	}
	if p, ok := c.d.Get("proxySettings"); ok && !isEmpty(p) {
		return ReasonChainedDialer
	}
	for _, n := range c.noises {
		if o, ok := n.(*ojson.Object); ok {
			if v, ok := o.Get("applyTo"); ok && !isEmpty(v) {
				return ReasonApplyTo
			}
		}
	}
	if r, ok := stringAt(c.d, "settings", "redirect"); ok && r != "" {
		if _, port, err := net.SplitHostPort(r); err == nil && port == "53" {
			return ReasonRedirect53
		}
	}
	for i := 0; i < c.d.Len(); i++ {
		key, val := c.d.At(i)
		switch key {
		case "tag", "protocol":
		case "settings":
			so := asObject(val)
			for j := 0; j < so.Len(); j++ {
				if k, v := so.At(j); k != "noises" && !isEmpty(v) {
					return ReasonFreedomHasOtherSettings
				}
			}
		default:
			if !isEmpty(val) {
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
		o, ok := n.(*ojson.Object)
		if !ok {
			return nil, fmt.Errorf("noise %d is not an object", i)
		}
		for j := 0; j < o.Len(); j++ {
			switch key, _ := o.At(j); key {
			case "type", "packet", "delay", "applyTo":
			default:
				return nil, fmt.Errorf("noise %d: unknown field %q", i, key)
			}
		}
		typ, _ := stringAt(o, "type")
		packet, ok := stringAt(o, "packet")
		if !ok || packet == "" {
			return nil, fmt.Errorf("noise %d: packet must be a non-empty string", i)
		}
		item := &ojson.Object{}
		switch typ {
		case "hex":
			p := strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, packet)
			if p == "" {
				return nil, fmt.Errorf("noise %d: hex packet is only whitespace", i)
			}
			if _, err := hex.DecodeString(p); err != nil {
				return nil, fmt.Errorf("noise %d: bad hex packet: %w", i, err)
			}
			item.Set("type", "hex")
			item.Set("packet", p)
		case "str":
			item.Set("type", "str")
			item.Set("packet", packet)
		case "base64":
			// Legacy accepts URL-safe and unpadded input; finalmask wants std padded.
			p := strings.TrimRight(strings.NewReplacer("-", "+", "_", "/").Replace(strings.TrimSpace(packet)), "=")
			raw, err := base64.RawStdEncoding.DecodeString(p)
			if err != nil {
				return nil, fmt.Errorf("noise %d: bad base64 packet: %w", i, err)
			}
			if len(raw) == 0 {
				return nil, fmt.Errorf("noise %d: base64 packet decodes to nothing", i)
			}
			item.Set("type", "base64")
			item.Set("packet", base64.StdEncoding.EncodeToString(raw))
		case "rand":
			if !randShape.MatchString(packet) {
				return nil, fmt.Errorf("noise %d: rand packet %q is not \"n\" or \"a-b\"", i, packet)
			}
			item.Set("rand", packet)
		default:
			return nil, fmt.Errorf("noise %d: unknown type %q", i, typ)
		}
		if d, ok := o.Get("delay"); ok {
			item.Set("delay", d)
		}
		out = append(out, item)
	}
	return out, nil
}

// appendNoise adds the noise mask as the last finalmask.udp item (nearest the
// wire), creating streamSettings.finalmask.udp when missing.
func appendNoise(x *ojson.Object, items []any) {
	ss := ensureObject(x, "streamSettings")
	fm := ensureObject(ss, "finalmask")
	udp, _ := fm.Get("udp")
	arr, _ := udp.([]any)
	settings := &ojson.Object{}
	settings.Set("reset", json.Number("0"))
	settings.Set("noise", items)
	mask := &ojson.Object{}
	mask.Set("type", "noise")
	mask.Set("settings", settings)
	fm.Set("udp", append(arr, mask))
}

// referenced reports whether anything but d's own "tag" and the dialerProxy
// of the sockopt objects in skip names tag. Routing balancer and observatory
// selectors match by prefix.
func referenced(root, d *ojson.Object, tag string, skip map[*ojson.Object]bool) bool {
	var walk func(v any, key string, owner *ojson.Object) bool
	walk = func(v any, key string, owner *ojson.Object) bool {
		switch t := v.(type) {
		case *ojson.Object:
			for i := 0; i < t.Len(); i++ {
				k, v := t.At(i)
				if walk(v, k, t) {
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
			if owner == d && key == "tag" || skip[owner] && key == "dialerProxy" {
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

func removeOutbound(root, d *ojson.Object) {
	obs := arrayAt(root, "outbounds")
	kept := make([]any, 0, len(obs))
	for _, ob := range obs {
		if o, ok := ob.(*ojson.Object); !ok || o != d {
			kept = append(kept, ob)
		}
	}
	root.Set("outbounds", kept)
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case *ojson.Object:
		return t.Len() == 0
	case []any:
		return len(t) == 0
	}
	return false
}

func asObject(v any) *ojson.Object {
	if o, ok := v.(*ojson.Object); ok {
		return o
	}
	return &ojson.Object{}
}

// objectAt follows keys through nested objects; a missing step yields an
// empty, detached object.
func objectAt(o *ojson.Object, keys ...string) *ojson.Object {
	for _, k := range keys {
		v, _ := o.Get(k)
		o = asObject(v)
	}
	return o
}

func ensureObject(o *ojson.Object, key string) *ojson.Object {
	if v, ok := o.Get(key); ok {
		if c, ok := v.(*ojson.Object); ok {
			return c
		}
	}
	c := &ojson.Object{}
	o.Set(key, c)
	return c
}

func arrayAt(o *ojson.Object, keys ...string) []any {
	v, _ := objectAt(o, keys[:len(keys)-1]...).Get(keys[len(keys)-1])
	arr, _ := v.([]any)
	return arr
}

func stringAt(o *ojson.Object, keys ...string) (string, bool) {
	v, _ := objectAt(o, keys[:len(keys)-1]...).Get(keys[len(keys)-1])
	s, ok := v.(string)
	return s, ok
}
