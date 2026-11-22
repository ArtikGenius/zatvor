package link

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Export writes n as a share link that Parse reads back to an equal Node
// (apart from Raw). A node of an unknown scheme exports as its Raw text.
func Export(n Node) string {
	switch n.Scheme {
	case "vless":
		return exportUserinfo(n, "id")
	case "trojan", "hysteria2":
		return exportUserinfo(n, "password")
	case "vmess":
		return exportVMess(n)
	case "ss":
		cred := n.Params["method"] + ":" + n.Params["password"]
		user := base64.RawURLEncoding.EncodeToString([]byte(cred))
		return n.Scheme + "://" + user + "@" + hostPort(n, false) + queryPart(n, "/", "method", "password") + fragment(n)
	case "socks":
		auth := ""
		if u := n.Params["user"]; u != "" {
			auth = escapeUserinfo(u) + ":" + escapeUserinfo(n.Params["password"]) + "@"
		}
		return "socks://" + auth + hostPort(n, false) + queryPart(n, "", "user", "password") + fragment(n)
	}
	return n.Raw
}

func exportUserinfo(n Node, key string) string {
	scheme := n.Scheme
	if scheme == "hysteria2" {
		return scheme + "://" + escapeUserinfo(n.Params[key]) + "@" + hostPort(n, true) + queryPart(n, "/", key, "ports") + fragment(n)
	}
	return scheme + "://" + escapeUserinfo(n.Params[key]) + "@" + hostPort(n, false) + queryPart(n, "", key) + fragment(n)
}

func exportVMess(n Node) string {
	var buf bytes.Buffer
	field := func(k, v string) {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
		if buf.Len() > 1 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	}
	buf.WriteByte('{')
	field("v", "2")
	field("ps", n.Remark)
	field("add", n.Address)
	field("port", strconv.Itoa(n.Port))
	for _, k := range []string{"id", "aid", "scy", "net", "type", "host", "path", "tls", "sni", "alpn", "fp"} {
		if v := n.Params[k]; v != "" {
			field(k, v)
		}
	}
	buf.WriteByte('}')
	return "vmess://" + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func hostPort(n Node, multi bool) string {
	host := n.Address
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := strconv.Itoa(n.Port)
	if multi && n.Params["ports"] != "" {
		port = n.Params["ports"]
	}
	return host + ":" + port
}

// queryPart writes the parameters except skip as a sorted query string,
// preceded by prefix when there is one.
func queryPart(n Node, prefix string, skip ...string) string {
	skipped := map[string]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	keys := make([]string, 0, len(n.Params))
	for k := range n.Params {
		if !skipped[k] {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = url.QueryEscape(k) + "=" + url.QueryEscape(n.Params[k])
	}
	return prefix + "?" + strings.Join(parts, "&")
}

func fragment(n Node) string {
	if n.Remark == "" {
		return ""
	}
	return "#" + url.PathEscape(n.Remark)
}

// escapeUserinfo percent-encodes everything but unreserved characters, so
// ':' and '@' inside a credential cannot be mistaken for separators.
func escapeUserinfo(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
