// Command a writes the client Xray config of TUN variant A: Xray's own `tun`
// inbound (Xray-core v26.11.9 keys, infra/conf/tun.go) with system routes,
// outbound interface binding and the Windows WFP leak filters, and a SOCKS
// outbound to the upstream Xray on loopback. One rule sends the process given
// by -direct-process straight out (the per-app rule check).
//
// Prototype only (stage 0c, task 8). Run by proto/tun/check/Check.ps1 on a
// CI runner; it only writes a file.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Addresses shared with variant B (proto/tun/b) so both fronts look the same
// to the system: the TUN gets .1, its DNS server is .2.
const (
	Gateway4 = "172.19.0.1/30"
	Gateway6 = "fdfe:dcba:9876::1/126"
	DNS4     = "172.19.0.2"
	DNS6     = "fdfe:dcba:9876::2"
)

// Config builds the client config. upstream is host:port of the upstream
// SOCKS server; directProcess is an absolute process path (forward slashes,
// as Xray's process matcher compares them) or empty.
func Config(tunName, upstreamHost string, upstreamPort int, directProcess, logDir string) map[string]any {
	cfg := map[string]any{
		"log": map[string]any{
			"loglevel": "info",
			"access":   filepath.Join(logDir, "client-a-access.log"),
			"error":    filepath.Join(logDir, "client-a-error.log"),
		},
		"inbounds": []any{
			map[string]any{
				"tag":      "tun-in",
				"protocol": "tun",
				"port":     0,
				"settings": map[string]any{
					"name":                   tunName,
					"mtu":                    1500,
					"gateway":                []string{Gateway4, Gateway6},
					"dns":                    []string{DNS4, DNS6},
					"autoSystemRoutingTable": []string{"0.0.0.0/0", "::/0"},
					"autoOutboundsInterface": "auto",
					"autoSystemWfpBlockLeak": []string{"dns", "misconfigtun"},
				},
			},
		},
		"outbounds": []any{
			map[string]any{
				"tag":      "proxy",
				"protocol": "socks",
				"settings": map[string]any{"address": upstreamHost, "port": upstreamPort},
			},
			map[string]any{"tag": "direct", "protocol": "freedom"},
		},
	}
	var rules []any
	if directProcess != "" {
		rules = append(rules, map[string]any{
			"type":        "field",
			"process":     []string{strings.ReplaceAll(directProcess, `\`, "/")},
			"outboundTag": "direct",
		})
	}
	rules = append(rules, map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": "proxy"})
	cfg["routing"] = map[string]any{"domainStrategy": "AsIs", "rules": rules}
	return cfg
}

func main() {
	out := flag.String("out", "", "file to write")
	name := flag.String("name", "zatvor-a", "TUN adapter name")
	host := flag.String("upstream-host", "127.0.0.1", "upstream SOCKS host")
	port := flag.Int("upstream-port", 10800, "upstream SOCKS port")
	direct := flag.String("direct-process", "", "absolute path of a process sent direct")
	logDir := flag.String("log-dir", ".", "directory for Xray logs")
	flag.Parse()
	if *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	b, err := json.MarshalIndent(Config(*name, *host, *port, *direct, *logDir), "", "  ")
	if err == nil {
		err = os.WriteFile(*out, append(b, '\n'), 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "a:", err)
		os.Exit(1)
	}
}
