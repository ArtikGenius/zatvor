package link

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Parse reads one share link. Unknown schemes give ErrUnsupported, encrypted
// ones ErrEncrypted; other errors name the scheme but never quote the line.
func Parse(line string) (Node, error) {
	line = strings.TrimSpace(line)
	i := strings.Index(line, "://")
	if i <= 0 {
		return Node{}, fmt.Errorf("%w: not a link", ErrUnsupported)
	}
	scheme, rest := strings.ToLower(line[:i]), line[i+3:]
	n := Node{Scheme: scheme, Params: map[string]string{}, Raw: line}
	switch scheme {
	case "happ":
		if strings.HasPrefix(strings.ToLower(rest), "crypt") {
			return Node{}, fmt.Errorf("%w: %s", ErrEncrypted, scheme)
		}
	case "incy":
		return Node{}, fmt.Errorf("%w: %s", ErrEncrypted, scheme)
	case "vless", "trojan":
		return parseUserinfoLink(n, rest)
	case "hysteria2", "hy2":
		n.Scheme = "hysteria2"
		return parseUserinfoLink(n, rest)
	case "vmess":
		return parseVMess(n, rest)
	case "ss":
		return parseSS(n, rest)
	case "socks", "socks5":
		n.Scheme = "socks"
		return parseSocks(n, rest)
	}
	return Node{}, fmt.Errorf("%w: %s", ErrUnsupported, printable(scheme))
}

// printable keeps a scheme fit to be shown in an error.
func printable(s string) string {
	if len(s) > 16 {
		return "?"
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.') {
			return "?"
		}
	}
	return s
}

func bad(scheme, msg string) error { return fmt.Errorf("link: %s: %s", scheme, msg) }

// splitLink cuts the remark (after the first '#') and the query (after the
// first '?') off rest and returns what is left, the query and the remark.
func splitLink(rest string) (body string, query map[string]string, remark string) {
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		remark = unescapePath(rest[i+1:])
		rest = rest[:i]
	}
	query = map[string]string{}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		for _, kv := range strings.Split(rest[i+1:], "&") {
			k, v, _ := strings.Cut(kv, "=")
			k = unescapeQuery(k)
			if k == "" {
				continue
			}
			if _, dup := query[k]; !dup {
				query[k] = unescapeQuery(v)
			}
		}
		rest = rest[:i]
	}
	return rest, query, remark
}

func unescapePath(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

func unescapeQuery(s string) string {
	if u, err := url.QueryUnescape(s); err == nil {
		return u
	}
	return s
}

// splitAuthority splits "userinfo@host:port/path" at the last '@' and drops
// the path.
func splitAuthority(body string) (userinfo, hostport string, hasUser bool) {
	if i := strings.LastIndexByte(body, '@'); i >= 0 {
		userinfo, body, hasUser = body[:i], body[i+1:], true
	}
	if i := strings.IndexByte(body, '/'); i >= 0 {
		body = body[:i]
	}
	return userinfo, body, hasUser
}

// parseHostPort reads "host:port", "[v6]:port" and, when multi is set,
// "host:443,8443" or "host:443-8000" (the first port is the port, the whole
// list is returned in ports).
func parseHostPort(hp string, multi bool) (host string, port int, ports string, err error) {
	var portStr string
	if strings.HasPrefix(hp, "[") {
		end := strings.IndexByte(hp, ']')
		if end < 0 || !strings.HasPrefix(hp[end+1:], ":") {
			return "", 0, "", fmt.Errorf("bad address")
		}
		host, portStr = hp[1:end], hp[end+2:]
	} else {
		i := strings.LastIndexByte(hp, ':')
		if i < 0 {
			return "", 0, "", fmt.Errorf("missing port")
		}
		host, portStr = hp[:i], hp[i+1:]
		if strings.Contains(host, ":") {
			return "", 0, "", fmt.Errorf("bad address")
		}
	}
	if host == "" {
		return "", 0, "", fmt.Errorf("missing address")
	}
	first := portStr
	if multi {
		if i := strings.IndexAny(portStr, ",-"); i >= 0 {
			first, ports = portStr[:i], portStr
		}
	}
	port, err = strconv.Atoi(first)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, "", fmt.Errorf("invalid port")
	}
	return host, port, ports, nil
}

func setHostPort(n *Node, hp string, multi bool) error {
	host, port, ports, err := parseHostPort(hp, multi)
	if err != nil {
		return bad(n.Scheme, err.Error())
	}
	n.Address, n.Port = host, port
	if ports != "" {
		n.Params["ports"] = ports
	}
	return nil
}

// parseUserinfoLink handles scheme://credential@host:port?query#remark for
// vless (credential is the id), trojan and hysteria2 (the password).
func parseUserinfoLink(n Node, rest string) (Node, error) {
	body, query, remark := splitLink(rest)
	userinfo, hp, hasUser := splitAuthority(body)
	if !hasUser || userinfo == "" {
		return Node{}, bad(n.Scheme, "missing credential")
	}
	if err := setHostPort(&n, hp, n.Scheme == "hysteria2"); err != nil {
		return Node{}, err
	}
	key := "password"
	if n.Scheme == "vless" {
		key = "id"
	}
	for k, v := range query {
		n.Params[k] = v
	}
	n.Params[key] = unescapePath(userinfo)
	n.Remark = remark
	return n, nil
}

// decodeBase64 accepts standard and URL-safe alphabets, padded or not.
func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

// jsonText renders a JSON scalar the way vmess links use it (strings and
// numbers are interchangeable there).
func jsonText(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

func parseVMess(n Node, rest string) (Node, error) {
	body, _, remark := splitLink(rest)
	raw, err := decodeBase64(unescapePath(body))
	if err != nil {
		return Node{}, bad(n.Scheme, "bad base64")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return Node{}, bad(n.Scheme, "bad JSON")
	}
	port, err := strconv.Atoi(jsonText(m["port"]))
	if err != nil || port < 1 || port > 65535 {
		return Node{}, bad(n.Scheme, "invalid port")
	}
	n.Address, n.Port = jsonText(m["add"]), port
	if n.Address == "" {
		return Node{}, bad(n.Scheme, "missing address")
	}
	n.Remark = jsonText(m["ps"])
	if n.Remark == "" {
		n.Remark = remark
	}
	for _, k := range []string{"id", "aid", "scy", "net", "type", "host", "path", "tls", "sni", "alpn", "fp"} {
		if v := jsonText(m[k]); v != "" {
			n.Params[k] = v
		}
	}
	if n.Params["id"] == "" {
		return Node{}, bad(n.Scheme, "missing credential")
	}
	return n, nil
}

// splitFirstColon splits "a:b" at the first colon and percent-decodes both
// halves, so a password may itself contain ':' (Shadowsocks 2022 keys).
func splitFirstColon(s string) (a, b string, ok bool) {
	a, b, ok = strings.Cut(s, ":")
	return unescapePath(a), unescapePath(b), ok
}

// credentials reads the userinfo of ss and socks links: either plain
// "user:pass" (percent-encoded) or base64 of it.
func credentials(userinfo string) (a, b string, ok bool) {
	if strings.Contains(userinfo, ":") {
		return splitFirstColon(userinfo)
	}
	raw, err := decodeBase64(unescapePath(userinfo))
	if err != nil {
		return "", "", false
	}
	return strings.Cut(string(raw), ":")
}

func parseSS(n Node, rest string) (Node, error) {
	body, query, remark := splitLink(rest)
	n.Remark = remark
	for k, v := range query {
		n.Params[k] = v
	}
	var userinfo, hp string
	if strings.Contains(body, "@") {
		userinfo, hp, _ = splitAuthority(body)
	} else { // legacy: base64(method:password@host:port)
		raw, err := decodeBase64(strings.TrimRight(body, "/"))
		if err != nil {
			return Node{}, bad(n.Scheme, "bad base64")
		}
		i := strings.LastIndexByte(string(raw), '@')
		if i < 0 {
			return Node{}, bad(n.Scheme, "missing address")
		}
		userinfo, hp = string(raw[:i]), string(raw[i+1:])
		if strings.Contains(userinfo, ":") { // already plain, do not decode twice
			method, pass, _ := strings.Cut(userinfo, ":")
			n.Params["method"], n.Params["password"] = method, pass
			userinfo = ""
		}
	}
	if userinfo != "" {
		method, pass, ok := credentials(userinfo)
		if !ok {
			return Node{}, bad(n.Scheme, "bad credential")
		}
		n.Params["method"], n.Params["password"] = method, pass
	}
	if n.Params["method"] == "" {
		return Node{}, bad(n.Scheme, "missing method")
	}
	if err := setHostPort(&n, hp, false); err != nil {
		return Node{}, err
	}
	return n, nil
}

func parseSocks(n Node, rest string) (Node, error) {
	body, query, remark := splitLink(rest)
	n.Remark = remark
	for k, v := range query {
		n.Params[k] = v
	}
	var userinfo, hp string
	hasUser := false
	if strings.Contains(body, "@") {
		userinfo, hp, hasUser = splitAuthority(body)
	} else if strings.Contains(body, ":") { // no credentials
		hp, _, _ = strings.Cut(body, "/")
	} else { // base64(user:pass@host:port)
		raw, err := decodeBase64(strings.TrimRight(body, "/"))
		if err != nil {
			return Node{}, bad(n.Scheme, "bad base64")
		}
		i := strings.LastIndexByte(string(raw), '@')
		if i < 0 {
			return Node{}, bad(n.Scheme, "missing address")
		}
		userinfo, hp, hasUser = string(raw[:i]), string(raw[i+1:]), true
		user, pass, ok := strings.Cut(userinfo, ":")
		if !ok {
			return Node{}, bad(n.Scheme, "bad credential")
		}
		n.Params["user"], n.Params["password"] = user, pass
		userinfo, hasUser = "", false
	}
	if hasUser {
		user, pass, ok := credentials(userinfo)
		if !ok {
			return Node{}, bad(n.Scheme, "bad credential")
		}
		n.Params["user"], n.Params["password"] = user, pass
	}
	if err := setHostPort(&n, hp, false); err != nil {
		return Node{}, err
	}
	return n, nil
}
