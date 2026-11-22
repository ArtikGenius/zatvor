// Package hwid computes the frozen per-provider device id (ADR 0008) and the
// request headers that carry it.
package hwid

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"golang.org/x/net/idna"
)

// key is part of the frozen algorithm; never change it (ADR 0008).
const key = "zatvor-hwid-v1"

// Device describes the optional x-device-* headers.
type Device struct{ OS, OSVersion, Model string }

// Options controls which optional headers are sent.
type Options struct{ SendDevice bool }

// NormalizeHost reduces a subscription host to its frozen form (ADR 0008). It
// accepts host, host:port, [v6] and [v6]:port (and a bare IPv6 address),
// lower-cases, strips port, brackets, an IPv6 zone and one trailing dot, and
// converts IDN to punycode (idna.Lookup; on error the lower-cased input is kept).
func NormalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if strings.HasPrefix(h, "[") {
		if end := strings.Index(h, "]"); end > 0 {
			h = h[1:end]
		}
	} else if strings.Count(h, ":") == 1 {
		h, _, _ = strings.Cut(h, ":")
	}
	h, _, _ = strings.Cut(h, "%")
	h = strings.TrimSuffix(h, ".")
	if strings.Contains(h, ":") {
		return h
	}
	if a, err := idna.Lookup.ToASCII(h); err == nil {
		return a
	}
	return h
}

// ID returns the first 32 lowercase hex chars of
// HMAC-SHA256(key "zatvor-hwid-v1", machineID || subscriptionHost), where || is
// plain byte concatenation. machineID is trimmed and lower-cased and the host
// passed through NormalizeHost before hashing (ADR 0008).
func ID(machineID, subscriptionHost string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(strings.ToLower(strings.TrimSpace(machineID))))
	m.Write([]byte(NormalizeHost(subscriptionHost)))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// Headers returns x-hwid (always) and, when opts.SendDevice is set, the
// non-empty x-device-os, x-ver-os and x-device-model headers.
func Headers(machineID, host string, dev Device, opts Options) http.Header {
	h := http.Header{}
	h.Set("X-Hwid", ID(machineID, host))
	if opts.SendDevice {
		set := func(k, v string) {
			if v != "" {
				h.Set(k, v)
			}
		}
		set("X-Device-Os", dev.OS)
		set("X-Ver-Os", dev.OSVersion)
		set("X-Device-Model", dev.Model)
	}
	return h
}
