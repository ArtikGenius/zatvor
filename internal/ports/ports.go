// Package ports chooses and re-validates the loopback ports of our
// listeners. Persisted ports are kept while they still bind; a busy port (taken
// by another app, or inside a Hyper-V/WSL excluded range) is re-picked.
package ports

import (
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strconv"

	"github.com/ArtikGenius/zatvor/internal/gen"
)

const (
	minPort = 20000
	maxPort = 60999
	// minGap keeps two of our ports out of one reserved block (K50).
	minGap = 1024
	// maxTries is the probe budget for one port.
	maxTries = 64
)

// ErrExhausted means no free port was found within the try budget.
var ErrExhausted = errors.New("ports: no free loopback port found")

// wellKnown are ports other proxy tools use; never picked (pitfalls 57, 58).
var wellKnown = map[int]bool{10808: true, 10809: true, 7890: true, 9090: true, 9097: true, 19090: true}

// Prober reports whether 127.0.0.1:port can be bound. Any non-nil error means
// the port is unusable and must be re-picked, whatever the cause (in use,
// access denied by an excluded range, ...).
type Prober func(port int) error

// LoopbackProber binds 127.0.0.1:port for TCP and for UDP (Mixed needs both
// for SOCKS UDP associate; checking both for every port is harmless) and
// releases the sockets at once.
func LoopbackProber(port int) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	l, err := net.Listen("tcp4", addr)
	if err != nil {
		return err
	}
	if err := l.Close(); err != nil {
		return err
	}
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}
	c, err := net.ListenUDP("udp4", ua)
	if err != nil {
		return err
	}
	return c.Close()
}

// Pick returns ports for all four listeners. A port of prev is kept when it is
// set, not a well-known port, distinct from the other kept ports, and probe
// binds it. A zero or unusable port is picked fresh in [20000, 60999], at least
// 1024 away from every other chosen port. The bool is true when any port
// changed.
func Pick(prev gen.Ports, probe Prober, rnd *rand.Rand) (gen.Ports, bool, error) {
	out := prev
	slots := []*int{&out.Mixed, &out.Probe, &out.DevTools, &out.API}
	var kept []int
	var need []*int
	for _, s := range slots {
		p := *s
		if p > 0 && p <= 65535 && !wellKnown[p] && !contains(kept, p) && probe(p) == nil {
			kept = append(kept, p)
		} else {
			need = append(need, s)
		}
	}
	for _, s := range need {
		p, err := pickOne(kept, probe, rnd)
		if err != nil {
			return prev, false, err
		}
		*s = p
		kept = append(kept, p)
	}
	return out, out != prev, nil
}

func pickOne(taken []int, probe Prober, rnd *rand.Rand) (int, error) {
	tries := 0
	// Candidates rejected without probing are cheap; bound them separately.
	for guard := 0; guard < 100*maxTries && tries < maxTries; guard++ {
		p := minPort + rnd.Intn(maxPort-minPort+1)
		if wellKnown[p] || tooClose(taken, p) {
			continue
		}
		tries++
		if probe(p) == nil {
			return p, nil
		}
	}
	return 0, fmt.Errorf("%w (after %d tries)", ErrExhausted, tries)
}

func tooClose(taken []int, p int) bool {
	for _, t := range taken {
		if d := p - t; d < minGap && d > -minGap {
			return true
		}
	}
	return false
}

func contains(s []int, p int) bool {
	for _, v := range s {
		if v == p {
			return true
		}
	}
	return false
}
