package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/ArtikGenius/zatvor/internal/happ"
	"github.com/ArtikGenius/zatvor/internal/link"
	"github.com/ArtikGenius/zatvor/internal/ojson"
	"github.com/ArtikGenius/zatvor/internal/subfetch"
)

// Diff lists profile IDs. Added and Changed follow the new order, Removed the
// old one. Changed means the same ID with a different Node (remark or any
// parameter) or a different JSON config; the ID covers the port, so a port
// change shows up as one Removed plus one Added.
type Diff struct {
	Added, Removed, Changed []string
	// Warnings are set by callers above Apply (subsvc): short codes, no secrets.
	Warnings []string
}

// ErrNoProfiles: the fetch succeeded but yielded no usable profile.
var ErrNoProfiles = errors.New("profile: subscription has no usable profiles")

// ProblemError: the fetch reported a problem. It names the problem code only.
type ProblemError struct{ Problem subfetch.Problem }

func (e *ProblemError) Error() string {
	return fmt.Sprintf("profile: subscription fetch problem %d", int(e.Problem))
}

// Apply merges a fetch result into old. On any problem, or when no usable
// profile results, it returns old unchanged with an error (spec 5.6).
// Existing profiles keep their user settings (RoutingSource, Disabled) by ID.
func Apply(old Subscription, res subfetch.Result, now time.Time) (Subscription, Diff, error) {
	if res.Problem != subfetch.None {
		return old, Diff{}, &ProblemError{res.Problem}
	}
	oldByID := make(map[string]Profile, len(old.Profiles))
	for _, p := range old.Profiles {
		oldByID[p.ID] = p
	}

	sub := old
	sub.Info = res.Info
	if res.Info.Title != "" {
		sub.Title = res.Info.Title
	}
	sub.LastGood = now
	// The routing header applies to links only; JSON subscriptions keep the
	// previous value untouched (spec 5.4 B4).
	if len(res.Body.Nodes) > 0 {
		sub.ProviderRouting = providerRouting(res, old.ProviderRouting)
	}

	var fresh []Profile
	seen := map[string]bool{}
	add := func(p Profile) {
		if seen[p.ID] {
			return
		}
		seen[p.ID] = true
		fresh = append(fresh, p)
	}
	for i := range res.Body.Nodes {
		n := res.Body.Nodes[i]
		add(Profile{ID: link.ID(n), Name: n.Remark, Node: &n})
	}
	for _, c := range res.Body.Configs {
		id, ok := ConfigID(c)
		if !ok {
			continue
		}
		add(Profile{ID: id, Name: configName(c), Config: c})
	}
	if len(fresh) == 0 {
		return old, Diff{}, ErrNoProfiles
	}

	var diff Diff
	for i := range fresh {
		p := &fresh[i]
		p.SubscriptionID = sub.ID
		prev, existed := oldByID[p.ID]
		if existed {
			p.RoutingSource = prev.RoutingSource
			p.Disabled = prev.Disabled
			if changed(prev, *p) {
				diff.Changed = append(diff.Changed, p.ID)
			}
		} else {
			diff.Added = append(diff.Added, p.ID)
			p.RoutingSource = defaultSource(sub, *p)
		}
		if p.RoutingSource.Kind == SourceProvider {
			p.RoutingSource.Happ = sub.ProviderRouting
		}
	}
	for _, p := range old.Profiles {
		if !seen[p.ID] {
			diff.Removed = append(diff.Removed, p.ID)
		}
	}
	sub.Profiles = fresh
	return sub, diff, nil
}

// providerRouting: an absent or disabled header clears the routing; a header
// that fails to decode keeps the previous one.
func providerRouting(res subfetch.Result, prev *happ.Profile) *happ.Profile {
	if !res.Info.RoutingEnabled || res.Info.RoutingLink == "" {
		return nil
	}
	p, err := happ.DecodeLink(res.Info.RoutingLink)
	if err != nil {
		return prev
	}
	return &p
}

// defaultSource: a new link profile follows the provider's routing when the
// subscription has one; everything else starts as proxy-all.
func defaultSource(sub Subscription, p Profile) Source {
	if p.Node != nil && sub.ProviderRouting != nil {
		return Source{Kind: SourceProvider}
	}
	return Source{Kind: SourcePreset, Preset: PresetAllProxy}
}

func changed(a, b Profile) bool {
	if (a.Node == nil) != (b.Node == nil) {
		return true
	}
	if a.Node != nil {
		x, y := *a.Node, *b.Node
		x.Raw, y.Raw = "", ""
		if len(x.Params) == 0 && len(y.Params) == 0 {
			x.Params, y.Params = nil, nil
		}
		return !reflect.DeepEqual(x, y)
	}
	xa, ea := json.Marshal(a.Config)
	xb, eb := json.Marshal(b.Config)
	return ea != nil || eb != nil || string(xa) != string(xb)
}

func configName(c *ojson.Object) string {
	if v, ok := c.Get("remarks"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	if ep, ok := proxyEndpoint(c); ok {
		return ep.address + ":" + ep.port
	}
	return ""
}

type endpoint struct{ protocol, address, port, cred, network string }

// notProxy lists outbound protocols that are never the proxy server.
var notProxy = map[string]bool{"freedom": true, "blackhole": true, "dns": true, "loopback": true}

// ConfigID identifies a provider JSON config by its proxy outbound: the first
// entry of "outbounds" whose protocol is not freedom, blackhole, dns or
// loopback. It hashes that outbound's protocol, address, port, credential
// (id, password or user) and streamSettings network, in the 16-hex format of
// link.ID. ok is false when the config has no such outbound.
func ConfigID(c *ojson.Object) (string, bool) {
	ep, ok := proxyEndpoint(c)
	if !ok {
		return "", false
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{"json", ep.protocol, strings.ToLower(ep.address), ep.port, ep.cred, ep.network}, "\x00")))
	return hex.EncodeToString(sum[:])[:16], true
}

func obj(v any) *ojson.Object { o, _ := v.(*ojson.Object); return o }

func first(v any) *ojson.Object {
	if a, ok := v.([]any); ok && len(a) > 0 {
		return obj(a[0])
	}
	return nil
}

func str(o *ojson.Object, key string) string {
	if o == nil {
		return ""
	}
	v, _ := o.Get(key)
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	}
	return ""
}

func proxyEndpoint(c *ojson.Object) (endpoint, bool) {
	v, _ := c.Get("outbounds")
	outs, _ := v.([]any)
	for _, ov := range outs {
		o := obj(ov)
		if o == nil {
			continue
		}
		proto := strings.ToLower(str(o, "protocol"))
		if proto == "" || notProxy[proto] {
			continue
		}
		settings := obj(mustGet(o, "settings"))
		// Server entry: vnext[0] (vless/vmess), servers[0] (trojan, ss,
		// socks), or the settings object itself (flat form).
		srv := first(mustGet(settings, "vnext"))
		if srv == nil {
			srv = first(mustGet(settings, "servers"))
		}
		if srv == nil {
			srv = settings
		}
		ep := endpoint{protocol: proto, address: str(srv, "address"), port: str(srv, "port")}
		user := first(mustGet(srv, "users"))
		for _, src := range []*ojson.Object{user, srv} {
			for _, k := range []string{"id", "password", "user"} {
				if ep.cred == "" {
					ep.cred = str(src, k)
				}
			}
		}
		ep.network = str(obj(mustGet(o, "streamSettings")), "network")
		return ep, true
	}
	return endpoint{}, false
}

func mustGet(o *ojson.Object, key string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(key)
	return v
}
