// Package hwid computes the frozen per-provider device id (ADR 0008) and the
// request headers that carry it.
package hwid

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

// key is part of the frozen algorithm; never change it (ADR 0008).
const key = "zatvor-hwid-v1"

// Device describes the optional x-device-* headers.
type Device struct{ OS, OSVersion, Model string }

// Options controls which optional headers are sent.
type Options struct{ SendDevice bool }

// ID returns the first 32 lowercase hex chars of
// HMAC-SHA256(key "zatvor-hwid-v1", machineID || subscriptionHost), where || is
// plain byte concatenation. subscriptionHost is the lower-case URL hostname
// without port (see ADR 0008); callers pass it in that form.
func ID(machineID, subscriptionHost string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(machineID))
	m.Write([]byte(subscriptionHost))
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
