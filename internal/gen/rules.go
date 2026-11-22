package gen

import "github.com/ArtikGenius/zatvor/internal/ojson"

// AIDomains is the default list sent through the proxy when the developer
// tools feature is on.
var AIDomains = []string{
	"domain:anthropic.com",
	"domain:claude.ai",
	"domain:claude.com",
	"domain:openai.com",
	"domain:chatgpt.com",
	"domain:oaistatic.com",
	"domain:oaiusercontent.com",
	"full:generativelanguage.googleapis.com",
	"domain:gemini.google.com",
	"domain:aistudio.google.com",
	"domain:cursor.sh",
	"domain:cursor.com",
}

// target is where a rule sends traffic: an outbound or a balancer.
type target struct {
	tag      string
	balancer bool
}

func (t target) key() string {
	if t.balancer {
		return "balancerTag"
	}
	return "outboundTag"
}

// rule builds {"type":"field", <match key>: <match>, <target>: tag}.
func rule(matchKey string, match any, t target) *ojson.Object {
	return kv("type", "field", matchKey, match, t.key(), t.tag)
}

func strList(l []string) []any {
	out := make([]any, 0, len(l))
	for _, s := range l {
		out = append(out, s)
	}
	return out
}

func nonEmpty(l []string) []string {
	var out []string
	for _, s := range l {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// internalRules is layer 1: the api inbound reaches the api, and the probe
// inbound goes to the profile's proxy target. Route checks get no rule here.
func internalRules(proxy target) []any {
	return []any{
		rule("inboundTag", []any{TagAPI}, target{tag: TagAPI}),
		rule("inboundTag", []any{TagProbe}, proxy),
	}
}

// blockRules is layer 2: user blocks go to the block outbound.
func blockRules(blocks []string, blockTag string) []any {
	blocks = nonEmpty(blocks)
	if len(blocks) == 0 {
		return nil
	}
	return []any{rule("domain", strList(blocks), target{tag: blockTag})}
}

// devToolRules is layer 4: the dev-tools inbound and the AI domains go to
// the proxy target. Layer 3 (per-app rules) is empty in stage 1a.
func devToolRules(on bool, domains []string, proxy target) []any {
	if !on {
		return nil
	}
	rules := []any{rule("inboundTag", []any{TagDevTools}, proxy)}
	if domains = nonEmpty(domains); len(domains) > 0 {
		rules = append(rules, rule("domain", strList(domains), proxy))
	}
	return rules
}

// bootstrapRules sends everything to the proxy; it follows every other
// prepended layer, so internal and block rules still win.
func bootstrapRules(on bool, proxy target) []any {
	if !on {
		return nil
	}
	return []any{rule("network", "tcp,udp", proxy)}
}

// prependedRules is the exact list placed before the source's rules.
func prependedRules(in Input, proxy target, blockTag string) []any {
	domains := in.AIDomains
	if len(domains) == 0 {
		domains = AIDomains
	}
	var out []any
	out = append(out, internalRules(proxy)...)
	out = append(out, blockRules(in.UserBlocks, blockTag)...)
	out = append(out, devToolRules(in.DevTools, domains, proxy)...)
	out = append(out, bootstrapRules(in.BootstrapAllProxy, proxy)...)
	return out
}

// isCatchAll reports whether a provider rule matches all traffic: it has a
// target and no condition except an all-network match.
func isCatchAll(r *ojson.Object) bool {
	_, hasOut := r.Get("outboundTag")
	_, hasBal := r.Get("balancerTag")
	if !hasOut && !hasBal {
		return false
	}
	for _, k := range r.Keys() {
		switch k {
		case "type", "outboundTag", "balancerTag", "ruleTag":
		case "network":
			v, _ := r.Get(k)
			if s, _ := v.(string); s != "tcp,udp" && s != "udp,tcp" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// rewriteInboundTags replaces every tag in from by to inside the rules'
// inboundTag lists, dropping repeats, and reports how many rules changed.
func rewriteInboundTags(rules []any, from map[string]bool, to string) int {
	changed := 0
	for _, r := range rules {
		o, ok := r.(*ojson.Object)
		if !ok {
			continue
		}
		v, ok := o.Get("inboundTag")
		if !ok {
			continue
		}
		list, ok := v.([]any)
		if !ok {
			continue
		}
		var out []any
		seen := map[string]bool{}
		hit := false
		for _, e := range list {
			s, isStr := e.(string)
			if isStr && from[s] {
				s, hit = to, true
			}
			if isStr {
				if seen[s] {
					continue
				}
				seen[s] = true
				out = append(out, s)
			} else {
				out = append(out, e)
			}
		}
		if hit {
			o.Set("inboundTag", out)
			changed++
		}
	}
	return changed
}
