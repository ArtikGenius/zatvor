// Package gen builds the Xray config Zatvor runs: our inbounds, api and log,
// the rule layers of spec 6.3, and the routing source (provider, preset or
// Happ profile). Every step is a pure function over ojson values.
package gen

import (
	"errors"
	"fmt"

	"github.com/ArtikGenius/zatvor/internal/happ"
	"github.com/ArtikGenius/zatvor/internal/link"
	"github.com/ArtikGenius/zatvor/internal/ojson"
	"github.com/ArtikGenius/zatvor/internal/profile"
)

var (
	// ErrInvalidInput marks a bad Input (ports, credentials, unknown preset).
	ErrInvalidInput = errors.New("gen: invalid input")
	// ErrInvalidConfig marks a provider JSON Build cannot work with.
	ErrInvalidConfig = errors.New("gen: invalid provider config")
)

// Ports are the loopback ports of our listeners, chosen and persisted by the
// daemon. DevTools is used only when Input.DevTools is set.
type Ports struct{ Mixed, Probe, DevTools, API int }

// Input is everything Build needs.
type Input struct {
	Profile           profile.Profile
	Ports             Ports
	ProbeUser         string
	ProbePass         string
	DevTools          bool
	AIDomains         []string // empty: the default AIDomains
	UserBlocks        []string
	SelectedSites     []string // selected-only preset
	BootstrapAllProxy bool
	LogLevel          string // empty: "warning"
}

// Report describes what Build did to the profile.
type Report struct {
	// ReplacedInbounds names the provider inbounds we dropped (tag, or
	// protocol and port when untagged).
	ReplacedInbounds []string
	// ProxyTag is the outbound, or balancer, that tunnel traffic and the
	// probe inbound go to.
	ProxyTag string
	Warnings []string
}

// Outbound tags we use for link nodes.
const (
	tagProxy  = "proxy"
	tagDirect = "direct"
	tagBlock  = "block"
)

var logLevels = map[string]bool{"debug": true, "info": true, "warning": true, "error": true, "none": true}

// Build produces the Xray config for in. Link nodes get outbounds proxy,
// direct and block. A full JSON keeps the provider's outbounds; with source
// Provider it also keeps routing (our rules come first, then the provider's
// rules in their own order and content, other routing keys untouched) and
// dns exactly, while its inbounds are replaced by ours. Its api section is
// replaced by ours and its metrics section is removed (a Report warning
// names it), so no provider section opens a listener.
//
// Inbound tags: our system-proxy inbound is tagged "mixed-in", except that
// it takes the tag of the first provider inbound whose protocol is socks,
// http or mixed (else of the first provider inbound), unless that tag is one
// of our reserved tags. Provider routing is never rewritten: rules naming
// other provider inbound tags stay as they are and, because those inbounds
// are replaced, will not match; a Report warning lists those tags.
//
// ProxyTag for a full JSON is the target (outboundTag or balancerTag) of the
// first catch-all rule, else the first outbound (Xray's default). If that
// outbound is freedom, blackhole, dns or loopback, the first outbound with
// any other protocol is used instead (an error if there is none). A catch-all
// is a rule with a target whose only other keys are type, ruleTag and
// network "tcp,udp"; any other condition makes it not a catch-all.
//
// A zero Source.Kind means SourceProvider. For a full JSON, Source.Happ is
// ignored under SourceProvider (the JSON's own routing is kept).
func Build(in Input) ([]byte, Report, error) {
	if err := validate(in); err != nil {
		return nil, Report{}, err
	}
	var (
		root *ojson.Object
		rep  Report
		err  error
	)
	p := in.Profile
	switch {
	case p.Node != nil && p.Config != nil:
		return nil, Report{}, fmt.Errorf("%w: profile has both a node and a config", ErrInvalidInput)
	case p.Node != nil:
		root, rep, err = buildLink(in)
	case p.Config != nil:
		root, rep, err = buildConfig(in)
	default:
		return nil, Report{}, fmt.Errorf("%w: profile has neither a node nor a config", ErrInvalidInput)
	}
	if err != nil {
		return nil, Report{}, err
	}
	out, err := ojson.Marshal(root)
	if err != nil {
		return nil, Report{}, err
	}
	return out, rep, nil
}

func validate(in Input) error {
	ports := []int{in.Ports.Mixed, in.Ports.Probe, in.Ports.API}
	if in.DevTools {
		ports = append(ports, in.Ports.DevTools)
	}
	seen := map[int]bool{}
	for _, p := range ports {
		if p < 1 || p > 65535 {
			return fmt.Errorf("%w: port %d out of range", ErrInvalidInput, p)
		}
		if seen[p] {
			return fmt.Errorf("%w: port %d used twice", ErrInvalidInput, p)
		}
		seen[p] = true
	}
	if in.ProbeUser == "" || in.ProbePass == "" {
		return fmt.Errorf("%w: probe credentials are empty", ErrInvalidInput)
	}
	if in.LogLevel != "" && !logLevels[in.LogLevel] {
		return fmt.Errorf("%w: log level %q", ErrInvalidInput, in.LogLevel)
	}
	return nil
}

func logLevel(in Input) string {
	if in.LogLevel == "" {
		return "warning"
	}
	return in.LogLevel
}

// sourceKind resolves the zero value to SourceProvider.
func sourceKind(s profile.Source) profile.SourceKind {
	if s.Kind == "" {
		return profile.SourceProvider
	}
	return s.Kind
}

// replacement is a routing source that replaces the profile's routing and
// dns: a preset or a Happ profile.
type replacement struct {
	routing, dns, fakedns *ojson.Object
	warnings              []string
}

// replacementFor builds the routing for a preset or Happ source. For
// SourceProvider it applies only when forLink is set (a share link has no
// routing of its own): the subscription's Happ profile, else all-proxy.
func replacementFor(in Input, proxy, direct, block string, forLink bool) (*replacement, error) {
	src := in.Profile.RoutingSource
	kind := sourceKind(src)
	r := &replacement{}
	preset := func(p profile.Preset) error {
		rt, err := presetRouting(p, proxy, direct, aiDomains(in), in.SelectedSites)
		r.routing = rt
		return err
	}
	switch kind {
	case profile.SourcePreset:
		if err := preset(src.Preset); err != nil {
			return nil, err
		}
	case profile.SourceHapp, profile.SourceProvider:
		if kind == profile.SourceProvider && !forLink {
			return nil, nil
		}
		if src.Happ == nil {
			if kind == profile.SourceHapp {
				return nil, fmt.Errorf("%w: happ source without a happ profile", ErrInvalidInput)
			}
			r.warnings = append(r.warnings, "link profile has no provider routing; using the all-through-proxy preset")
			if err := preset(profile.PresetAllProxy); err != nil {
				return nil, err
			}
			break
		}
		rt, dns, err := happ.Translate(*src.Happ, proxy, direct, block)
		if err != nil {
			return nil, err
		}
		r.routing, r.dns, r.fakedns = rt, dns, happ.FakeDNS(*src.Happ)
	default:
		return nil, fmt.Errorf("%w: unknown routing source %q", ErrInvalidInput, kind)
	}
	return r, nil
}

func aiDomains(in Input) []string {
	if len(in.AIDomains) == 0 {
		return AIDomains
	}
	return in.AIDomains
}

// withLayers returns routing whose rules are our prepended layers followed by
// the routing's own rules (in order, content untouched).
func withLayers(routing *ojson.Object, in Input, proxy target, block string) (*ojson.Object, error) {
	var own []any
	if v, ok := routing.Get("rules"); ok {
		l, isList := v.([]any)
		if !isList {
			return nil, fmt.Errorf("%w: routing.rules is not a list", ErrInvalidConfig)
		}
		own = l
	}
	rules := append(prependedRules(in, proxy, block), own...)
	routing.Set("rules", rules)
	return routing, nil
}

func buildLink(in Input) (*ojson.Object, Report, error) {
	n := in.Profile.Node
	ob, err := link.Outbound(*n, tagProxy)
	if err != nil {
		return nil, Report{}, err
	}
	rep := Report{ProxyTag: tagProxy}
	rp, err := replacementFor(in, tagProxy, tagDirect, tagBlock, true)
	if err != nil {
		return nil, Report{}, err
	}
	rep.Warnings = append(rep.Warnings, rp.warnings...)
	routing, err := withLayers(rp.routing, in, target{tag: tagProxy}, tagBlock)
	if err != nil {
		return nil, Report{}, err
	}

	root := &ojson.Object{}
	root.Set("log", logSection(logLevel(in)))
	root.Set("api", apiSection())
	root.Set("stats", &ojson.Object{})
	if rp.dns != nil {
		root.Set("dns", rp.dns)
	}
	if rp.fakedns != nil {
		root.Set("fakedns", rp.fakedns)
	}
	root.Set("inbounds", inbounds(in, TagMixed, rp.fakedns != nil))
	root.Set("outbounds", []any{ob, freedom(tagDirect), blackhole(tagBlock)})
	root.Set("routing", routing)
	return root, rep, nil
}

func freedom(tag string) *ojson.Object   { return kv("tag", tag, "protocol", "freedom") }
func blackhole(tag string) *ojson.Object { return kv("tag", tag, "protocol", "blackhole") }

func buildConfig(in Input) (*ojson.Object, Report, error) {
	cv, err := clone(in.Profile.Config)
	if err != nil {
		return nil, Report{}, err
	}
	root := cv.(*ojson.Object)
	var rep Report

	// Outbounds: kept as sent; direct and block added only when missing.
	ov, _ := root.Get("outbounds")
	outs, ok := ov.([]any)
	if !ok || len(outs) == 0 {
		return nil, Report{}, fmt.Errorf("%w: no outbounds", ErrInvalidConfig)
	}
	directTag, outs := ensureOutbound(outs, tagDirect, "freedom", freedom)
	blockTag, outs := ensureOutbound(outs, tagBlock, "blackhole", blackhole)

	routing, _ := root.Get("routing")
	rt, _ := routing.(*ojson.Object)
	proxy, err := providerTarget(rt, outs)
	if err != nil {
		return nil, Report{}, err
	}

	// Preset and Happ sources replace routing and dns entirely.
	replaced := sourceKind(in.Profile.RoutingSource) != profile.SourceProvider
	if replaced && proxy.balancer {
		// The replaced routing drops the provider's balancers.
		proxy = target{tag: firstOutboundTag(outs, directTag, blockTag)}
		if proxy.tag == "" {
			return nil, Report{}, fmt.Errorf("%w: no tagged proxy outbound", ErrInvalidConfig)
		}
	}
	if !proxy.balancer {
		t, err := realProxy(proxy.tag, outs)
		if err != nil {
			return nil, Report{}, err
		}
		proxy = target{tag: t}
	}
	var rp *replacement
	if replaced {
		rp, err = replacementFor(in, proxy.tag, directTag, blockTag, false)
		if err != nil {
			return nil, Report{}, err
		}
	}
	rep.ProxyTag = proxy.tag

	var fakedns bool
	if rp != nil {
		rep.Warnings = append(rep.Warnings, rp.warnings...)
		root.Delete("dns")
		root.Delete("fakedns")
		if rp.dns != nil {
			root.Set("dns", rp.dns)
		}
		if rp.fakedns != nil {
			root.Set("fakedns", rp.fakedns)
			fakedns = true
		}
		rt = rp.routing
	} else {
		_, fakedns = root.Get("fakedns")
		if rt == nil {
			rt = &ojson.Object{}
		}
	}

	// Inbounds: replaced. Our mixed inbound takes the tag of the first
	// provider inbound that is socks, http or mixed (else the first one), so
	// rules naming it still match. Provider routing is never rewritten.
	mixedTag := TagMixed
	if iv, ok := root.Get("inbounds"); ok {
		old, _ := iv.([]any)
		rep.ReplacedInbounds = describeInbounds(old)
		keep := chooseMixedTag(old)
		if keep != "" && !reservedTag(keep) {
			mixedTag = keep
		}
		if rp == nil {
			var lost []string
			for _, t := range referencedInboundTags(rt, old) {
				if t != mixedTag {
					lost = append(lost, t)
				}
			}
			if len(lost) > 0 {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf(
					"provider routing rules naming inbound tags %v will not match: those inbounds are replaced", lost))
			}
		}
	}

	rt, err = withLayers(rt, in, proxy, blockTag)
	if err != nil {
		return nil, Report{}, err
	}
	// Root sections that open a listener of their own: only ours may.
	// api is replaced below; metrics would serve expvar and pprof.
	for _, k := range listenerSections {
		if _, ok := root.Get(k); ok {
			root.Delete(k)
			rep.Warnings = append(rep.Warnings, fmt.Sprintf(
				"provider %q section removed: it opens a listener Zatvor does not control", k))
		}
	}
	root.Set("log", logSection(logLevel(in)))
	root.Set("api", apiSection())
	root.Set("stats", &ojson.Object{})
	root.Set("inbounds", inbounds(in, mixedTag, fakedns))
	root.Set("outbounds", outs)
	root.Set("routing", rt)
	return root, rep, nil
}

// listenerSections are provider root sections that make Xray listen on an
// address outside our inbounds (Xray 26.11: metrics.listen is an HTTP server
// with expvar and pprof). api also has a listen field, but it is always
// replaced by ours.
var listenerSections = []string{"metrics"}

func reservedTag(t string) bool {
	return t == TagAPI || t == TagProbe || t == TagDevTools
}

// describeInbounds names the provider inbounds: the tag, or protocol:port
// when untagged.
func describeInbounds(list []any) []string {
	var names []string
	for _, v := range list {
		o, ok := v.(*ojson.Object)
		if !ok {
			continue
		}
		if tag, _ := getString(o, "tag"); tag != "" {
			names = append(names, tag)
			continue
		}
		proto, _ := getString(o, "protocol")
		port, _ := o.Get("port")
		names = append(names, fmt.Sprintf("%s:%v", proto, port))
	}
	return names
}

// chooseMixedTag is the tag of the first provider inbound whose protocol is
// socks, http or mixed, else of the first provider inbound ("" if untagged).
func chooseMixedTag(list []any) string {
	first := ""
	for i, v := range list {
		o, ok := v.(*ojson.Object)
		if !ok {
			continue
		}
		tag, _ := getString(o, "tag")
		if i == 0 {
			first = tag
		}
		switch p, _ := getString(o, "protocol"); p {
		case "socks", "http", "mixed":
			return tag
		}
	}
	return first
}

// referencedInboundTags lists, in provider order, the provider inbound tags
// that routing rules name in inboundTag.
func referencedInboundTags(rt *ojson.Object, inbounds []any) []string {
	have := map[string]bool{}
	for _, v := range inbounds {
		if o, ok := v.(*ojson.Object); ok {
			if t, _ := getString(o, "tag"); t != "" {
				have[t] = true
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	v, _ := rt.Get("rules")
	rules, _ := v.([]any)
	for _, r := range rules {
		o, ok := r.(*ojson.Object)
		if !ok {
			continue
		}
		iv, _ := o.Get("inboundTag")
		list, _ := iv.([]any)
		for _, e := range list {
			if t, ok := e.(string); ok && have[t] && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

func getString(o *ojson.Object, key string) (string, bool) {
	v, ok := o.Get(key)
	s, isStr := v.(string)
	return s, ok && isStr
}

// ensureOutbound returns the tag of a usable outbound of the given protocol,
// appending one under a non-clashing tag when the provider has none named
// want.
func ensureOutbound(outs []any, want, protocol string, mk func(string) *ojson.Object) (string, []any) {
	used := map[string]bool{}
	for _, v := range outs {
		o, ok := v.(*ojson.Object)
		if !ok {
			continue
		}
		tag, _ := getString(o, "tag")
		used[tag] = true
		if p, _ := getString(o, "protocol"); tag == want && p == protocol {
			return tag, outs
		}
	}
	tag := want
	for i := 1; used[tag]; i++ {
		tag = fmt.Sprintf("zatvor-%s-%d", want, i)
	}
	return tag, append(outs, mk(tag))
}

// providerTarget is the outbound or balancer the provider's first catch-all
// rule points at (it shadows later ones), else Xray's default, the first
// outbound.
func providerTarget(routing *ojson.Object, outs []any) (target, error) {
	if routing != nil {
		if v, ok := routing.Get("rules"); ok {
			rules, _ := v.([]any)
			for i := 0; i < len(rules); i++ {
				r, ok := rules[i].(*ojson.Object)
				if !ok || !isCatchAll(r) {
					continue
				}
				if t, ok := getString(r, "balancerTag"); ok && t != "" {
					return target{tag: t, balancer: true}, nil
				}
				if t, ok := getString(r, "outboundTag"); ok && t != "" {
					return target{tag: t}, nil
				}
			}
		}
	}
	if o, ok := outs[0].(*ojson.Object); ok {
		if t, _ := getString(o, "tag"); t != "" {
			return target{tag: t}, nil
		}
	}
	return target{}, fmt.Errorf("%w: first outbound has no tag", ErrInvalidConfig)
}

// realProxy returns tag unless its outbound is freedom, blackhole, dns or
// loopback (direct, block or internal), in which case it returns the first
// outbound with another protocol.
func realProxy(tag string, outs []any) (string, error) {
	other := ""
	for _, v := range outs {
		o, ok := v.(*ojson.Object)
		if !ok {
			continue
		}
		t, _ := getString(o, "tag")
		p, _ := getString(o, "protocol")
		internal := p == "freedom" || p == "blackhole" || p == "dns" || p == "loopback"
		if t == tag && !internal {
			return tag, nil
		}
		if other == "" && t != "" && !internal {
			other = t
		}
	}
	if other == "" {
		return "", fmt.Errorf("%w: no proxy outbound (only freedom, blackhole, dns or loopback)", ErrInvalidConfig)
	}
	return other, nil
}

// firstOutboundTag is the first tagged outbound other than direct and block.
func firstOutboundTag(outs []any, direct, block string) string {
	for _, v := range outs {
		if o, ok := v.(*ojson.Object); ok {
			if t, _ := getString(o, "tag"); t != "" && t != direct && t != block {
				return t
			}
		}
	}
	return ""
}

// clone deep-copies a parsed JSON value.
func clone(v any) (any, error) {
	switch x := v.(type) {
	case *ojson.Object:
		out := &ojson.Object{}
		for i := 0; i < x.Len(); i++ {
			k, val := x.At(i)
			c, err := clone(val)
			if err != nil {
				return nil, err
			}
			out.Set(k, c)
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			c, err := clone(e)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	default:
		return v, nil
	}
}
