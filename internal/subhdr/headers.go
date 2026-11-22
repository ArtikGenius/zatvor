// Package subhdr parses subscription response headers (and Happ-style inline
// "#key: value" headers from the body) leniently into Info.
package subhdr

import (
	"encoding/base64"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Info is the provider metadata. Total==0 means unlimited; zero Expire means none.
type Info struct {
	Upload, Download, Total int64
	Expire                  time.Time
	Title                   string
	UpdateInterval          time.Duration
	SupportURL, WebPageURL  string
	Announce                string
	RoutingLink             string
	RoutingEnabled          bool
	HWIDNotSupported        bool
	HWIDLimitReached        bool
	// HappLike: the response carries Happ-oriented headers (spec §5.3 hint).
	HappLike bool
}

const maxText = 256

// Parse reads h; inline (lower-cased keys, from subbody) fills gaps, real
// headers win.
func Parse(h http.Header, inline map[string]string, contentDisposition string) Info {
	get := func(name string) string {
		if v := strings.TrimSpace(h.Get(name)); v != "" {
			return v
		}
		return strings.TrimSpace(inline[name])
	}
	var i Info
	parseUserinfo(&i, get("subscription-userinfo"))

	i.Title = decodeText(get("profile-title"))
	if i.Title == "" {
		i.Title = dispositionName(contentDisposition)
	}
	if n, err := strconv.ParseFloat(get("profile-update-interval"), 64); err == nil && n > 0 && n < 24*365 {
		i.UpdateInterval = time.Duration(n * float64(time.Hour))
	}
	i.SupportURL = capText(get("support-url"))
	i.WebPageURL = capText(get("profile-web-page-url"))
	i.Announce = decodeText(get("announce"))

	i.RoutingLink = get("routing")
	if i.RoutingLink != "" {
		switch strings.ToLower(get("routing-enable")) {
		case "false", "0":
		default:
			i.RoutingEnabled = true
		}
	}

	i.HWIDNotSupported = isTrue(get("x-hwid-not-supported"))
	i.HWIDLimitReached = isTrue(get("x-hwid-max-devices-reached")) || isTrue(get("x-hwid-limit"))

	i.HappLike = i.RoutingLink != "" || get("announce") != "" || get("hide-settings") != "" ||
		hasPrefixKey(h, inline, "sub-info-")
	return i
}

func isTrue(v string) bool {
	v = strings.ToLower(v)
	return v == "true" || v == "1"
}

func hasPrefixKey(h http.Header, inline map[string]string, prefix string) bool {
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), prefix) {
			return true
		}
	}
	for k := range inline {
		if strings.HasPrefix(strings.ToLower(k), prefix) {
			return true
		}
	}
	return false
}

func parseUserinfo(i *Info, v string) {
	for _, part := range strings.Split(v, ";") {
		k, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil || n < 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "upload":
			i.Upload = n
		case "download":
			i.Download = n
		case "total":
			i.Total = n
		case "expire":
			if n > 0 {
				i.Expire = time.Unix(n, 0).UTC()
			}
		}
	}
}

// decodeText handles "base64:<std|url, padded or not>" and plain UTF-8.
func decodeText(v string) string {
	if rest, ok := strings.CutPrefix(v, "base64:"); ok {
		rest = strings.TrimSpace(rest)
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if b, err := enc.DecodeString(rest); err == nil && utf8.Valid(b) {
				return capText(string(b))
			}
		}
		return ""
	}
	if !utf8.ValidString(v) {
		return ""
	}
	return capText(v)
}

func capText(s string) string {
	if len(s) <= maxText {
		return s
	}
	r := []rune(s)
	for len(string(r)) > maxText {
		r = r[:len(r)-1]
	}
	return string(r)
}

// dispositionName extracts the filename (RFC 5987 filename* first).
func dispositionName(cd string) string {
	if cd == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		return ""
	}
	// mime.ParseMediaType already decodes filename* into params["filename"].
	name := params["filename"]
	if !utf8.ValidString(name) {
		return ""
	}
	return capText(strings.TrimSpace(name))
}
