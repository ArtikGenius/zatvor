package engine

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/ArtikGenius/zatvor/internal/subfetch"
)

// DefaultCheckServices are the public IP-check services of the tunnel
// check: HTTPS, none in .ru, three independent operators (spec R3.10).
var DefaultCheckServices = []string{"https://api.ipify.org", "https://ifconfig.co/ip", "https://icanhazip.com"}

// Status is a tri-state check outcome. Only OK is good: the zero value and
// Unknown never count as OK (pitfall K13).
type Status string

const (
	Unknown Status = "unknown" // too few answers to decide
	OK      Status = "ok"      // two services agree on the exit address
	Bad     Status = "bad"     // services of one address family disagree
)

// CheckResult is one tunnel check. IPv4/IPv6 hold the agreed exit address
// of that family, only when Status is OK. Gen is the engine's network
// generation the check ran in.
type CheckResult struct {
	Status Status    `json:"status"`
	IPv4   string    `json:"ipv4,omitempty"`
	IPv6   string    `json:"ipv6,omitempty"`
	At     time.Time `json:"at"`
	Gen    uint64    `json:"gen"`
}

const (
	checkTimeout = 10 * time.Second
	maxIPBody    = 256
)

// TunnelCheck asks every service, in parallel and through dial (the
// authenticated probe inbound), for the caller's address. A service answers
// when it returns 200 with a body that is an IPv4 or IPv6 address after
// trimming space; anything else is no answer. The result is OK once two
// services name the same address, Bad when at least two answers of one
// family exist and no two of them agree, and Unknown otherwise (fewer than
// two answers, or answers of different families only). It returns as soon
// as two services agree. Each request has its own 10 s limit within ctx.
func TunnelCheck(ctx context.Context, dial subfetch.Dialer, services []string) CheckResult {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         dial,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: checkTimeout,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	answers := make(chan netip.Addr, len(services))
	var wg sync.WaitGroup
	for _, svc := range services {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a, ok := askIP(ctx, client, svc); ok {
				answers <- a
			}
		}()
	}
	go func() { wg.Wait(); close(answers) }()

	votes := map[netip.Addr]int{}
	var got []netip.Addr
	for a := range answers {
		got = append(got, a)
		votes[a]++
		if votes[a] >= 2 {
			cancel()
			res := CheckResult{Status: OK, At: time.Now()}
			if a.Is4() {
				res.IPv4 = a.String()
			} else {
				res.IPv6 = a.String()
			}
			return res
		}
	}
	v4, v6 := 0, 0
	for _, a := range got {
		if a.Is4() {
			v4++
		} else {
			v6++
		}
	}
	if v4 >= 2 || v6 >= 2 { // same-family answers, none agreeing
		return CheckResult{Status: Bad, At: time.Now()}
	}
	return CheckResult{Status: Unknown, At: time.Now()}
}

func askIP(ctx context.Context, c *http.Client, url string) (netip.Addr, bool) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return netip.Addr{}, false
	}
	resp, err := c.Do(req)
	if err != nil {
		return netip.Addr{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxIPBody+1))
	if err != nil || len(b) > maxIPBody {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(strings.TrimSpace(string(b)))
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}
