package main

import (
	"bytes"
	"net/netip"
	"regexp"
	"strings"
)

// Finding is one suspected secret. It never carries the matched text, so
// reports cannot leak what they flag.
type Finding struct {
	Path string
	Line int
	Rule string
}

// AllowList is a set of literals (UUIDs, addresses, keys, hostnames) that are
// known to be fake or public and must not be reported.
type AllowList map[string]struct{}

// ParseAllowList reads one literal per line; blank lines and "#" comments
// (whole-line or trailing, after whitespace) are ignored.
func ParseAllowList(data []byte) AllowList {
	a := AllowList{}
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "#"); i >= 0 && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line != "" {
			a[line] = struct{}{}
		}
	}
	return a
}

func (a AllowList) has(s string) bool {
	_, ok := a[s]
	return ok
}

var (
	subscriptionRe = regexp.MustCompile(`(?i)https?://([a-z0-9.-]+)(?::\d+)?/(?:sub|subscription|subscribe|api/v1/client/subscribe)[/?][^\s"'<>)\]]+`)
	uuidRe         = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	ipv4Re         = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	privateKeyRe   = regexp.MustCompile(`(?i)"privateKey"\s*:\s*"([A-Za-z0-9+/_-]{43}=?)"`)
	iniKeyRe       = regexp.MustCompile(`(?im)^\s*(?:PrivateKey|PresharedKey|HeaderProtectionKey)\s*=\s*([A-Za-z0-9+/_-]{43}=?)\s*$`)
	pemRe          = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)

	exampleHosts = []string{"example.net", "example.com", "example.org", "localhost"}
	docNets      = []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
	}
)

func isExampleHost(h string) bool {
	h = strings.ToLower(h)
	for _, e := range exampleHosts {
		if h == e || strings.HasSuffix(h, "."+e) {
			return true
		}
	}
	return false
}

func isPublicIPv4(s string) bool {
	ip, err := netip.ParseAddr(s)
	if err != nil || !ip.Is4() {
		return false // e.g. 999.1.1.1 or a version number like 1.2.3.400
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsMulticast() {
		return false
	}
	for _, p := range docNets {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func inTestdata(path string) bool {
	p := "/" + strings.ReplaceAll(path, `\`, "/")
	return strings.Contains(p, "/testdata/")
}

// Scan checks content of the file at path and returns one Finding per
// suspected secret. The ipv4 rule applies only to files under testdata/.
func Scan(path string, content []byte, allow AllowList) []Finding {
	var out []Finding
	add := func(line int, rule string) { out = append(out, Finding{Path: path, Line: line, Rule: rule}) }
	checkIP := inTestdata(path)

	for i, raw := range bytes.Split(content, []byte("\n")) {
		line, n := string(raw), i+1

		for _, m := range subscriptionRe.FindAllStringSubmatch(line, -1) {
			if !isExampleHost(m[1]) && !allow.has(m[0]) && !allow.has(m[1]) {
				add(n, "subscription-url")
			}
		}
		for _, m := range uuidRe.FindAllString(line, -1) {
			if !allow.has(m) && !allow.has(strings.ToLower(m)) {
				add(n, "uuid")
			}
		}
		if checkIP {
			for _, m := range ipv4Re.FindAllString(line, -1) {
				if isPublicIPv4(m) && !allow.has(m) {
					add(n, "ipv4")
				}
			}
		}
		for _, m := range privateKeyRe.FindAllStringSubmatch(line, -1) {
			if !allow.has(m[1]) {
				add(n, "private-key")
			}
		}
		for _, m := range iniKeyRe.FindAllStringSubmatch(line, -1) {
			if !allow.has(m[1]) {
				add(n, "ini-key")
			}
		}
		if pemRe.MatchString(line) {
			add(n, "private-key")
		}
	}
	return out
}
