// Package link parses proxy share links (vless, vmess, trojan, ss, hysteria2,
// socks) into a Node, writes a Node back as a link, and maps a Node to an Xray
// outbound. Errors name the scheme only, never the link: links carry secrets.
package link

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

var (
	// ErrUnsupported marks a link whose scheme this package does not know.
	ErrUnsupported = errors.New("link: unsupported scheme")
	// ErrEncrypted marks a link that is encrypted for one client (happ://crypt*,
	// INCY) and cannot be read without that client's key.
	ErrEncrypted = errors.New("link: encrypted link")
	// ErrInsecureTLS marks a node that asks to skip TLS verification without a
	// pinned certificate; Xray no longer supports allowInsecure.
	ErrInsecureTLS = errors.New("link: insecure TLS without a pinned certificate is not supported")
	// ErrMalformed marks a link of a known scheme that cannot be read: bad
	// address, port, base64, JSON or credential.
	ErrMalformed = errors.New("link: malformed link")
)

// Node is one proxy server as found in a share link. Params carries every
// query and userinfo field under its share-link name (id, flow, security, sni,
// fp, pbk, sid, spx, type, path, host, mode, extra, serviceName, alpn,
// insecure, obfs, obfs-password, pinSHA256, method, password, user, aid, ...).
// For vmess, the fields of the base64 JSON are kept under their own names
// (net, type, host, path, tls, sni, alpn, fp, scy, aid).
type Node struct {
	Scheme  string
	Remark  string
	Address string
	Port    int
	Params  map[string]string
	Raw     string
}

// ID is a stable identity of the server and how to reach it: the first 16 hex
// characters of the SHA-256 of scheme, address, port, credential and
// transport. The remark and unrelated parameters do not change it, so a
// subscription refresh keeps the selected node (pitfall 46).
func ID(n Node) string {
	parts := []string{n.Scheme, strings.ToLower(n.Address), strconv.Itoa(n.Port)}
	for _, k := range []string{"id", "password", "user", "type", "net", "security", "tls", "path", "host", "serviceName"} {
		parts = append(parts, n.Params[k])
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}
