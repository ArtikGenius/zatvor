// Package happ translates Happ routing profiles into Xray routing and dns
// objects. Format reference: docs/research/subscriptions.md section 3.
package happ

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ArtikGenius/zatvor/internal/ojson"
)

// Profile is a Happ routing profile. All values are strings in the wire
// format, including booleans and timestamps; JSON names are exact.
type Profile struct {
	Name              string            `json:"Name"`
	GlobalProxy       string            `json:"GlobalProxy"`
	UseChunkFiles     string            `json:"UseChunkFiles"`
	RemoteDns         string            `json:"RemoteDns"`
	DomesticDns       string            `json:"DomesticDns"`
	RemoteDNSType     string            `json:"RemoteDNSType"`
	RemoteDNSDomain   string            `json:"RemoteDNSDomain"`
	RemoteDNSIP       string            `json:"RemoteDNSIP"`
	DomesticDNSType   string            `json:"DomesticDNSType"`
	DomesticDNSDomain string            `json:"DomesticDNSDomain"`
	DomesticDNSIP     string            `json:"DomesticDNSIP"`
	Geoipurl          string            `json:"Geoipurl"`
	Geositeurl        string            `json:"Geositeurl"`
	LastUpdated       string            `json:"LastUpdated"`
	DnsHosts          map[string]string `json:"DnsHosts"`
	RouteOrder        string            `json:"RouteOrder"`
	DirectSites       []string          `json:"DirectSites"`
	DirectIp          []string          `json:"DirectIp"`
	ProxySites        []string          `json:"ProxySites"`
	ProxyIp           []string          `json:"ProxyIp"`
	BlockSites        []string          `json:"BlockSites"`
	BlockIp           []string          `json:"BlockIp"`
	DomainStrategy    string            `json:"DomainStrategy"`
	FakeDNS           string            `json:"FakeDNS"`
}

var linkPrefixes = []string{"happ://routing/onadd/", "happ://routing/add/"}

// DecodeLink decodes happ://routing/onadd/<b64> and happ://routing/add/<b64>.
// The payload may be standard or URL-safe base64, padded or not.
func DecodeLink(link string) (Profile, error) {
	link = strings.TrimSpace(link)
	for _, prefix := range linkPrefixes {
		if payload, ok := strings.CutPrefix(link, prefix); ok {
			raw, err := decodeBase64(payload)
			if err != nil {
				return Profile{}, fmt.Errorf("happ routing link: %w", err)
			}
			return DecodeJSON(raw)
		}
	}
	return Profile{}, errors.New("happ routing link: unsupported scheme or path")
}

func decodeBase64(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		case '+':
			return '-'
		case '/':
			return '_'
		}
		return r
	}, s)
	s = strings.TrimRight(s, "=")
	if s == "" {
		return nil, errors.New("empty payload")
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("payload is not valid base64")
	}
	return b, nil
}

// DecodeJSON parses a routing profile JSON object. Unknown keys are ignored.
func DecodeJSON(data []byte) (Profile, error) {
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return Profile{}, fmt.Errorf("happ routing profile: %w", err)
	}
	return p, nil
}

// GeoURLs returns the geoip and geosite .dat URLs named by the profile
// (empty when the profile names none).
func GeoURLs(p Profile) (geoip, geosite string) {
	return p.Geoipurl, p.Geositeurl
}

// Translate builds the Xray routing and dns objects for the profile. Rule
// groups follow RouteOrder (default block-direct-proxy); within a group the
// domain rule comes before the ip rule. Unmatched traffic goes to proxyTag
// unless GlobalProxy is "false", then to directTag.
func Translate(p Profile, proxyTag, directTag, blockTag string) (routing, dns *ojson.Object, err error) {
	tags := map[string]string{"block": blockTag, "direct": directTag, "proxy": proxyTag}
	lists := map[string][2][]string{
		"block":  {p.BlockSites, p.BlockIp},
		"direct": {p.DirectSites, p.DirectIp},
		"proxy":  {p.ProxySites, p.ProxyIp},
	}

	rules := []any{}
	for _, group := range routeOrder(p.RouteOrder) {
		domains, ips := lists[group][0], lists[group][1]
		if len(domains) > 0 {
			rules = append(rules, fieldRule("domain", domains, tags[group]))
		}
		if len(ips) > 0 {
			rules = append(rules, fieldRule("ip", ips, tags[group]))
		}
	}
	final := proxyTag
	if strings.EqualFold(strings.TrimSpace(p.GlobalProxy), "false") {
		final = directTag
	}
	rules = append(rules, fieldRule("network", "tcp,udp", final))

	strategy := p.DomainStrategy
	if strategy == "" {
		strategy = "IPIfNonMatch"
	}
	routing = &ojson.Object{}
	routing.Set("domainStrategy", strategy)
	routing.Set("rules", rules)

	servers := []any{}
	if len(p.DirectSites) > 0 {
		s := &ojson.Object{}
		s.Set("address", dnsAddress(p.DomesticDNSType, p.DomesticDNSDomain, p.DomesticDNSIP, "8.8.8.8"))
		s.Set("domains", strs(p.DirectSites))
		servers = append(servers, s)
	}
	remote := &ojson.Object{}
	remote.Set("address", dnsAddress(p.RemoteDNSType, p.RemoteDNSDomain, p.RemoteDNSIP, "1.1.1.1"))
	remote.Set("skipFallback", false)
	servers = append(servers, remote)

	dns = &ojson.Object{}
	if len(p.DnsHosts) > 0 {
		hosts := &ojson.Object{}
		names := make([]string, 0, len(p.DnsHosts))
		for n := range p.DnsHosts {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			hosts.Set(n, p.DnsHosts[n])
		}
		dns.Set("hosts", hosts)
	}
	dns.Set("queryStrategy", "UseIP")
	dns.Set("servers", servers)
	dns.Set("tag", "dns_out")
	return routing, dns, nil
}

// routeOrder returns the dash-separated groups of s; unknown or repeated
// segments are dropped and missing groups appended so no list is lost.
func routeOrder(s string) []string {
	var order []string
	seen := map[string]bool{}
	for _, seg := range strings.Split(strings.ToLower(s), "-") {
		seg = strings.TrimSpace(seg)
		if (seg == "block" || seg == "direct" || seg == "proxy") && !seen[seg] {
			seen[seg] = true
			order = append(order, seg)
		}
	}
	for _, g := range []string{"block", "direct", "proxy"} {
		if !seen[g] {
			order = append(order, g)
		}
	}
	return order
}

func fieldRule(key string, val any, outbound string) *ojson.Object {
	r := &ojson.Object{}
	r.Set("type", "field")
	if l, ok := val.([]string); ok {
		val = strs(l)
	}
	r.Set(key, val)
	r.Set("outboundTag", outbound)
	return r
}

func strs(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

// dnsAddress picks the Xray dns server address: the profile's domain/URL when
// given, else an https dns-query URL for DoH, else the plain IP (DoU).
func dnsAddress(typ, domain, ip, defaultIP string) string {
	if domain != "" {
		return domain
	}
	if ip == "" {
		ip = defaultIP
	}
	if strings.EqualFold(typ, "DoH") {
		return "https://" + ip + "/dns-query"
	}
	return ip
}
