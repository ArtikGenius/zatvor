package gen

import (
	"encoding/json"
	"fmt"

	"github.com/ArtikGenius/zatvor/internal/ojson"
)

// Tags of the inbounds and the api section that Zatvor owns.
const (
	// TagMixed is the default tag of the system-proxy inbound. A full JSON
	// whose provider inbounds shared one tag keeps that tag instead (see
	// Build).
	TagMixed    = "mixed-in"
	TagProbe    = "probe-in"
	TagDevTools = "devtools-in"
	TagAPI      = "api"
)

// loopback is the only address Zatvor ever listens on.
const loopback = "127.0.0.1"

var apiServices = []any{"HandlerService", "StatsService", "LoggerService"}

// kv builds an ordered object from alternating keys and values.
func kv(pairs ...any) *ojson.Object {
	o := &ojson.Object{}
	for i := 0; i+1 < len(pairs); i += 2 {
		o.Set(pairs[i].(string), pairs[i+1])
	}
	return o
}

func num(n int) json.Number { return json.Number(fmt.Sprint(n)) }

func sniffing(fakedns bool) *ojson.Object {
	dest := []any{"http", "tls", "quic"}
	if fakedns {
		dest = append(dest, "fakedns")
	}
	return kv("enabled", true, "destOverride", dest)
}

// mixedInbound is the unauthenticated system-proxy inbound (SOCKS and HTTP on
// one port).
func mixedInbound(tag string, port int, fakedns bool) *ojson.Object {
	return kv(
		"tag", tag,
		"listen", loopback,
		"port", num(port),
		"protocol", "mixed",
		"settings", kv("auth", "noauth", "udp", true),
		"sniffing", sniffing(fakedns),
	)
}

// probeInbound is the authenticated SOCKS inbound for Zatvor's own requests.
func probeInbound(port int, user, pass string) *ojson.Object {
	return kv(
		"tag", TagProbe,
		"listen", loopback,
		"port", num(port),
		"protocol", "socks",
		"settings", kv(
			"auth", "password",
			"accounts", []any{kv("user", user, "pass", pass)},
			"udp", true,
		),
	)
}

func apiInbound(port int) *ojson.Object {
	return kv(
		"tag", TagAPI,
		"listen", loopback,
		"port", num(port),
		"protocol", "dokodemo-door",
		"settings", kv("address", loopback),
	)
}

// inbounds returns our inbound list in a fixed order: mixed, probe,
// optional dev-tools, api.
func inbounds(in Input, mixedTag string, fakedns bool) []any {
	list := []any{
		mixedInbound(mixedTag, in.Ports.Mixed, fakedns),
		probeInbound(in.Ports.Probe, in.ProbeUser, in.ProbePass),
	}
	if in.DevTools {
		list = append(list, mixedInbound(TagDevTools, in.Ports.DevTools, fakedns))
	}
	return append(list, apiInbound(in.Ports.API))
}

func apiSection() *ojson.Object {
	return kv("tag", TagAPI, "services", apiServices)
}

func logSection(level string) *ojson.Object {
	return kv("loglevel", level)
}
