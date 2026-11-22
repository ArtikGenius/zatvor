package xrayrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// fallbackWorkers bounds the temporary Xray processes of the per-outbound
// fallback.
const fallbackWorkers = 4

// Latency measures, through the Xray core and not ICMP, how long an HTTP GET
// of url takes via each outbound. One temporary Xray runs with a socks
// inbound on 127.0.0.1 (random per-run credentials) per outbound, routed to
// that outbound only. RTT is the time until the response headers arrive; any
// HTTP status counts as reachable. If that shared Xray fails for a config
// reason (one outbound is invalid, so Xray exits at start), each outbound is
// measured with its own temporary Xray (at most four at a time), so only the
// outbounds that fail on their own carry an error. Other start failures (no
// binary, a cancelled ctx, a ready timeout) are given to every outbound. Every temporary Xray is stopped on
// every path.
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
	m, err := measure(ctx, xrayPath, assetDir, outbounds, url, timeout)
	if err == nil || len(outbounds) == 1 || !configFailure(ctx, err) {
		for i := range res {
			if err != nil {
				res[i].Err = err
			} else {
				res[i].RTT, res[i].Err = m[i].RTT, m[i].Err
			}
		}
		return res
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, fallbackWorkers)
	for i, o := range outbounds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			one, err := measure(ctx, xrayPath, assetDir, []*ojson.Object{o}, url, timeout)
			if err != nil {
				res[i].Err = err
				return
			}
			res[i].RTT, res[i].Err = one[0].RTT, one[0].Err
		}()
	}
	wg.Wait()
	return res
}

// errLatencyConfig marks a temporary config that could not be built.
var errLatencyConfig = errors.New("xrayrun: latency config")

// configFailure reports whether a failed shared run points at the config,
// so that measuring each outbound alone can help: the config could not be
// built, or Xray exited before it was ready (it rejects a bad outbound at
// start). A missing binary, a busy port, a ready timeout or a cancelled ctx
// would fail every single run the same way.
func configFailure(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	return errors.Is(err, errLatencyConfig) || errors.Is(err, ErrExited)
}

// measure runs one temporary Xray for outbounds and probes each of them.
// The error is set when that Xray could not be set up or started; the
// per-outbound results are valid only without it.
func measure(ctx context.Context, xrayPath, assetDir string, outbounds []*ojson.Object, url string, timeout time.Duration) ([]LatencyResult, error) {
	ports, err := freePorts(len(outbounds))
	if err != nil {
		return nil, err
	}
	user, pass := randHex(12), randHex(16)
	cfg, err := latencyConfig(outbounds, ports, user, pass)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errLatencyConfig, err)
	}

	r := New(xrayPath, assetDir, nil)
	checks := make([]ReadyCheck, len(ports))
	for i, p := range ports {
		checks[i] = SOCKSReady(fmt.Sprintf("127.0.0.1:%d", p), user, pass)
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
		return nil, err
	}
	defer r.Stop(3 * time.Second)

	res := make([]LatencyResult, len(outbounds))
	var wg sync.WaitGroup
	for i := range outbounds {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res[i].RTT, res[i].Err = probe(ctx, fmt.Sprintf("127.0.0.1:%d", ports[i]), user, pass, url, timeout)
		}(i)
	}
	wg.Wait()
	return res, nil
}

// socksAttemptTimeout bounds one SOCKSReady attempt, so a foreign listener
// that never answers cannot stall the polling.
const socksAttemptTimeout = 500 * time.Millisecond

// SOCKSReady is a ReadyCheck that is satisfied only by our own SOCKS5
// inbound at addr, for at most 10 seconds. With a user, the inbound must
// answer the greeting 05 01 02 with 05 02 and accept the RFC 1929
// credentials (01 00). With an empty user it must be a no-auth inbound (such
// as the mixed one): 05 01 00 answered with 05 00. A refused connection,
// another reply, EOF or a short read means "not ready yet". Freed ports can
// be taken by foreign listeners, so a plain connect is not proof that Xray
// is up.
func SOCKSReady(addr, user, pass string) ReadyCheck {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, readyTimeout)
		defer cancel()
		for {
			err := socksHandshake(ctx, addr, user, pass)
			if err == nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("xray not ready at %s: %w", addr, err)
			case <-time.After(readyInterval):
			}
		}
	}
}

func socksHandshake(ctx context.Context, addr, user, pass string) error {
	ctx, cancel := context.WithTimeout(ctx, socksAttemptTimeout)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	dl, _ := ctx.Deadline()
	if err := c.SetDeadline(dl); err != nil {
		return err
	}
	method := byte(2) // username/password
	if user == "" {
		method = 0 // no authentication
	}
	if _, err := c.Write([]byte{5, 1, method}); err != nil {
		return err
	}
	var rep [2]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil {
		return err
	}
	if rep != [2]byte{5, method} {
		return errors.New("not a socks5 server with the expected auth method")
	}
	if user == "" {
		return nil
	}
	req := append([]byte{1, byte(len(user))}, user...)
	req = append(req, byte(len(pass)))
	req = append(req, pass...)
	if _, err := c.Write(req); err != nil {
		return err
	}
	if _, err := io.ReadFull(c, rep[:]); err != nil {
		return err
	}
	if rep != [2]byte{1, 0} {
		return errors.New("socks5 credentials rejected")
	}
	return nil
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
