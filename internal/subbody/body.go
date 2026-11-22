// Package subbody detects and decodes a subscription response body. Detection
// is by body only (spec 5.2). Nothing here quotes a link: links carry secrets.
package subbody

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/ArtikGenius/zatvor/internal/link"
	"github.com/ArtikGenius/zatvor/internal/ojson"
)

// Kind is the detected shape of a body.
type Kind int

const (
	Unknown Kind = iota
	XrayJSON
	Links
	Clash
	Encrypted
	Empty
)

// Skip is one body line that did not become a node, or one element of a JSON
// array that did not become a config. Scheme is the text before "://" when
// printable, never the line itself.
type Skip struct {
	Line    int // 1-based body line (link bodies)
	Element int // 1-based array position (JSON array bodies)
	Scheme  string
	Reason  string
}

// Result is the outcome of Decode.
type Result struct {
	Kind    Kind
	Configs []*ojson.Object
	Nodes   []link.Node
	Skipped []Skip
	// InlineHeaders holds "#key: value" lines; keys are lower-cased, values trimmed.
	InlineHeaders map[string]string
}

var bom = []byte("\xef\xbb\xbf")

// Decode classifies and decodes a subscription body.
func Decode(body []byte) Result {
	body = bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(body), bom))
	if len(body) == 0 {
		return Result{Kind: Empty}
	}
	if body[0] == '{' || body[0] == '[' {
		return decodeJSON(body)
	}
	text := string(body)
	if dec, ok := decodeBase64(text); ok {
		text = dec
	}
	return decodeLines(text)
}

// decodeJSON reads one Xray config or an array of them. A bad array element
// is skipped and recorded; a single object must be a config.
func decodeJSON(body []byte) Result {
	v, err := ojson.Parse(body)
	if err != nil {
		return Result{Kind: Unknown}
	}
	if o, ok := v.(*ojson.Object); ok {
		if _, has := o.Get("outbounds"); !has {
			return Result{Kind: Unknown}
		}
		return Result{Kind: XrayJSON, Configs: []*ojson.Object{o}}
	}
	arr, _ := v.([]any)
	res := Result{Kind: Unknown}
	for i, it := range arr {
		o, ok := it.(*ojson.Object)
		if !ok {
			res.Skipped = append(res.Skipped, Skip{Element: i + 1, Reason: "not an object"})
			continue
		}
		if _, has := o.Get("outbounds"); !has {
			res.Skipped = append(res.Skipped, Skip{Element: i + 1, Reason: "no outbounds"})
			continue
		}
		res.Configs = append(res.Configs, o)
	}
	if len(res.Configs) > 0 {
		res.Kind = XrayJSON
	}
	return res
}

// decodeBase64 tries the four base64 alphabets on the whitespace-stripped body
// and accepts a result only if it holds a line with a known scheme.
func decodeBase64(s string) (string, bool) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		raw, err := enc.DecodeString(s)
		if err != nil {
			continue
		}
		out := strings.TrimPrefix(string(raw), string(bom))
		for _, line := range strings.Split(out, "\n") {
			if hasKnownScheme(line) {
				return out, true
			}
		}
	}
	return "", false
}

func hasKnownScheme(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return false
	}
	_, err := link.Parse(line)
	return err == nil || !errors.Is(err, link.ErrUnsupported)
}

func decodeLines(text string) Result {
	res := Result{}
	encrypted, clash := 0, false
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line[0] == '#' {
			if k, v, ok := strings.Cut(line[1:], ":"); ok {
				k = strings.ToLower(strings.TrimSpace(k))
				if k != "" && !strings.ContainsAny(k, " \t") {
					if res.InlineHeaders == nil {
						res.InlineHeaders = map[string]string{}
					}
					res.InlineHeaders[k] = strings.TrimSpace(v)
				}
			}
			continue
		}
		if line == "proxies:" || strings.HasPrefix(line, "proxies:") && strings.TrimSpace(line[len("proxies:"):]) == "" {
			clash = true
		}
		n, err := link.Parse(line)
		switch {
		case err == nil:
			res.Nodes = append(res.Nodes, n)
		case errors.Is(err, link.ErrEncrypted):
			encrypted++
			res.Skipped = append(res.Skipped, Skip{Line: i + 1, Scheme: schemeOf(line), Reason: "encrypted"})
		case errors.Is(err, link.ErrUnsupported):
			res.Skipped = append(res.Skipped, Skip{Line: i + 1, Scheme: schemeOf(line), Reason: "unsupported"})
		default:
			res.Skipped = append(res.Skipped, Skip{Line: i + 1, Scheme: schemeOf(line), Reason: "malformed"})
		}
	}
	switch {
	case clash && len(res.Nodes) == 0:
		res.Kind = Clash
	case len(res.Nodes) > 0:
		res.Kind = Links
	case encrypted > 0 && encrypted == len(res.Skipped):
		res.Kind = Encrypted
	case len(res.Skipped) == 0 && len(res.InlineHeaders) == 0:
		res.Kind = Empty
	default:
		res.Kind = Unknown
	}
	return res
}

// schemeOf returns the lower-cased text before "://" if it is a short plain
// scheme, else "".
func schemeOf(line string) string {
	i := strings.Index(line, "://")
	if i <= 0 || i > 16 {
		return ""
	}
	s := strings.ToLower(line[:i])
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.') {
			return ""
		}
	}
	return s
}
