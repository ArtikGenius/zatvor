package link

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ArtikGenius/zatvor/internal/ojson"
)

func obj(kv ...any) *ojson.Object {
	o := &ojson.Object{}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// setIf sets key only when val is not empty.
func setIf(o *ojson.Object, key, val string) {
	if val != "" {
		o.Set(key, val)
	}
}

func isTrue(s string) bool { return s == "1" || strings.EqualFold(s, "true") }

// Outbound maps n to an Xray (v26.11.9) outbound object with the given tag.
func Outbound(n Node, tag string) (*ojson.Object, error) {
	p := n.Params
	out := obj("tag", tag)
	switch n.Scheme {
	case "vless":
		user := obj("id", p["id"], "encryption", "none")
		if e := p["encryption"]; e != "" {
			user.Set("encryption", e)
		}
		setIf(user, "flow", p["flow"])
		out.Set("protocol", "vless")
		out.Set("settings", obj("vnext", []any{obj("address", n.Address, "port", n.Port, "users", []any{user})}))
	case "vmess":
		aid, _ := strconv.Atoi(p["aid"])
		sec := p["scy"]
		if sec == "" {
			sec = "auto"
		}
		user := obj("id", p["id"], "alterId", aid, "security", sec)
		out.Set("protocol", "vmess")
		out.Set("settings", obj("vnext", []any{obj("address", n.Address, "port", n.Port, "users", []any{user})}))
	case "trojan":
		out.Set("protocol", "trojan")
		out.Set("settings", obj("servers", []any{obj("address", n.Address, "port", n.Port, "password", p["password"])}))
	case "ss":
		if p["plugin"] != "" {
			return nil, fmt.Errorf("%w: ss plugin", ErrUnsupported)
		}
		out.Set("protocol", "shadowsocks")
		out.Set("settings", obj("servers", []any{obj("address", n.Address, "port", n.Port, "method", p["method"], "password", p["password"])}))
	case "socks":
		srv := obj("address", n.Address, "port", n.Port)
		if p["user"] != "" {
			srv.Set("users", []any{obj("user", p["user"], "pass", p["password"])})
		}
		out.Set("protocol", "socks")
		out.Set("settings", obj("servers", []any{srv}))
		return out, nil
	case "hysteria2":
		out.Set("protocol", "hysteria")
		out.Set("settings", obj("version", 2, "address", n.Address, "port", n.Port))
		out.Set("streamSettings", hysteriaStream(n))
		return out, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, printable(n.Scheme))
	}
	ss, err := stream(n)
	if err != nil {
		return nil, err
	}
	out.Set("streamSettings", ss)
	return out, nil
}

func hysteriaStream(n Node) *ojson.Object {
	p := n.Params
	sni := p["sni"]
	if sni == "" {
		sni = n.Address
	}
	tls := obj("serverName", sni, "alpn", []any{"h3"})
	if isTrue(p["insecure"]) {
		tls.Set("allowInsecure", true)
	}
	if pin := p["pinSHA256"]; pin != "" {
		tls.Set("pinnedPeerCertSha256", []any{strings.ToLower(strings.ReplaceAll(pin, ":", ""))})
	}
	ss := obj("network", "hysteria", "security", "tls", "tlsSettings", tls,
		"hysteriaSettings", obj("version", 2, "auth", p["password"]))
	if strings.EqualFold(p["obfs"], "salamander") {
		mask := obj("type", "salamander", "settings", obj("password", p["obfs-password"]))
		ss.Set("finalmask", obj("udp", []any{mask}))
	}
	return ss
}

// stream builds streamSettings for the TCP-based protocols.
func stream(n Node) (*ojson.Object, error) {
	p := n.Params
	netKey, security := "type", p["security"]
	if n.Scheme == "vmess" {
		netKey = "net"
		security = "none"
		if p["tls"] == "tls" {
			security = "tls"
		}
	} else if security == "" && n.Scheme == "trojan" {
		security = "tls"
	}
	if security == "" {
		security = "none"
	}
	network := p[netKey]
	switch network {
	case "", "raw":
		network = "tcp"
	case "splithttp":
		network = "xhttp"
	case "tcp", "ws", "grpc", "xhttp", "httpupgrade":
	default:
		return nil, fmt.Errorf("%w: transport %q", ErrUnsupported, network)
	}
	ss := obj("network", network, "security", security)
	switch security {
	case "none":
	case "tls":
		tls := &ojson.Object{}
		setIf(tls, "serverName", p["sni"])
		setIf(tls, "fingerprint", p["fp"])
		if a := p["alpn"]; a != "" {
			list := []any{}
			for _, s := range strings.Split(a, ",") {
				list = append(list, strings.TrimSpace(s))
			}
			tls.Set("alpn", list)
		}
		if isTrue(p["allowInsecure"]) || isTrue(p["insecure"]) {
			tls.Set("allowInsecure", true)
		}
		ss.Set("tlsSettings", tls)
	case "reality":
		fp := p["fp"]
		if fp == "" {
			fp = "chrome"
		}
		r := obj("serverName", p["sni"], "fingerprint", fp, "publicKey", p["pbk"])
		setIf(r, "shortId", p["sid"])
		setIf(r, "spiderX", p["spx"])
		ss.Set("realitySettings", r)
	default:
		return nil, fmt.Errorf("%w: security %q", ErrUnsupported, security)
	}
	switch network {
	case "ws":
		ws := &ojson.Object{}
		setIf(ws, "path", p["path"])
		setIf(ws, "host", p["host"])
		ss.Set("wsSettings", ws)
	case "httpupgrade":
		hu := &ojson.Object{}
		setIf(hu, "path", p["path"])
		setIf(hu, "host", p["host"])
		ss.Set("httpupgradeSettings", hu)
	case "grpc":
		name, mode := p["serviceName"], p["mode"]
		if n.Scheme == "vmess" { // vmess v2 JSON: path is the service, type the mode
			name, mode = p["path"], p["type"]
		}
		ss.Set("grpcSettings", obj("serviceName", name, "multiMode", mode == "multi"))
	case "xhttp":
		x := &ojson.Object{}
		setIf(x, "path", p["path"])
		setIf(x, "host", p["host"])
		setIf(x, "mode", p["mode"])
		if e := p["extra"]; e != "" {
			v, err := ojson.Parse([]byte(e))
			if err != nil {
				return nil, fmt.Errorf("link: %s: bad xhttp extra", n.Scheme)
			}
			eo, ok := v.(*ojson.Object)
			if !ok {
				return nil, fmt.Errorf("link: %s: xhttp extra is not an object", n.Scheme)
			}
			x.Set("extra", eo)
		}
		ss.Set("xhttpSettings", x)
	}
	return ss, nil
}
