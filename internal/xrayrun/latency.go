package xrayrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"github.com/ArtikGenius/zatvor/internal/ojson"
)

// DefaultLatencyURL is used when Latency gets an empty url.
const DefaultLatencyURL = "https://www.gstatic.com/generate_204"

// LatencyResult is the outcome for one outbound, in input order.
type LatencyResult struct {
	Tag string
	RTT time.Duration
	Err error
}

// Latency measures, through the Xray core and not ICMP, how long an HTTP GET
// of url takes via each outbound. One temporary Xray runs with a socks
// inbound on 127.0.0.1 (random per-run credentials) per outbound, routed to
// that outbound only. RTT is the time until the response headers arrive; any
// HTTP status counts as reachable. If the temporary Xray cannot start (for
// instance because one outbound is invalid), every result carries that
// error. The temporary Xray is stopped on every path.
func Latency(ctx context.Context, xrayPath, assetDir string, outbounds []*ojson.Object, url string, timeout time.Duration) []LatencyResult {
	res := make([]LatencyResult, len(outbounds))
	for i, o := range outbounds {
		res[i].Tag = tagOf(o)
	}
	if len(outbounds) == 0 {
		return res
	}
	if url == "" {
		url = DefaultLatencyURL
	}
	failAll := func(err error) []LatencyResult {
		for i := range res {
			res[i].Err = err
		}
		return res
	}

	ports, err := freePorts(len(outbounds))
	if err != nil {
		return failAll(err)
	}
	user, pass := randHex(12), randHex(16)
	cfg, err := latencyConfig(outbounds, ports, user, pass)
	if err != nil {
		return failAll(err)
	}

	r := New(xrayPath, assetDir, nil)
	checks := make([]ReadyCheck, len(ports))
	for i, p := range ports {
		checks[i] = DialReady(fmt.Sprintf("127.0.0.1:%d", p))
	}
	ready := func(ctx context.Context) error {
		for _, c := range checks {
			if err := c(ctx); err != nil {
				return err
			}
		}
		return nil
	}
	if err := r.Start(ctx, cfg, ready); err != nil {
		return failAll(err)
	}
	defer r.Stop(3 * time.Second)

	var wg sync.WaitGroup
	for i := range outbounds {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res[i].RTT, res[i].Err = probe(ctx, fmt.Sprintf("127.0.0.1:%d", ports[i]), user, pass, url, timeout)
		}(i)
	}
	wg.Wait()
	return res
}

func probe(ctx context.Context, socksAddr, user, pass, url string, timeout time.Duration) (time.Duration, error) {
	d, err := proxy.SOCKS5("tcp", socksAddr, &proxy.Auth{User: user, Password: pass}, proxy.Direct)
	if err != nil {
		return 0, err
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return 0, errors.New("socks dialer has no context support")
	}
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         cd.DialContext,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: timeout,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	rtt := time.Since(start)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return rtt, nil
}

func tagOf(o *ojson.Object) string {
	if o == nil {
		return ""
	}
	if v, ok := o.Get("tag"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// latencyConfig builds the temporary config with a serializer. Outbounds are
// copied and re-tagged so caller tags never clash.
func latencyConfig(outbounds []*ojson.Object, ports []int, user, pass string) ([]byte, error) {
	type m = map[string]any
	var inbounds, outs, rules []any
	for i, o := range outbounds {
		if o == nil {
			return nil, fmt.Errorf("outbound %d is nil", i)
		}
		in, out := fmt.Sprintf("lat-in-%d", i), fmt.Sprintf("lat-out-%d", i)
		inbounds = append(inbounds, m{
			"tag": in, "listen": "127.0.0.1", "port": ports[i], "protocol": "socks",
			"settings": m{"auth": "password", "udp": false, "accounts": []any{m{"user": user, "pass": pass}}},
		})
		cp := ojson.Object{}
		for j := 0; j < o.Len(); j++ {
			k, v := o.At(j)
			cp.Set(k, v)
		}
		cp.Set("tag", out)
		outs = append(outs, &cp)
		rules = append(rules, m{"type": "field", "inboundTag": []string{in}, "outboundTag": out})
	}
	return json.Marshal(m{
		"log":       m{"loglevel": "warning"},
		"inbounds":  inbounds,
		"outbounds": outs,
		"routing":   m{"rules": rules},
	})
}

func freePorts(n int) ([]int, error) {
	var lns []net.Listener
	defer func() {
		for _, l := range lns {
			l.Close()
		}
	}()
	ports := make([]int, 0, n)
	for i := 0; i < n; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		lns = append(lns, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
