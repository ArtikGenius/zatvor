package subsvc

import (
	"context"
	"sync"
	"time"

	"github.com/ArtikGenius/zatvor/internal/profile"
)

const (
	defaultInterval = 24 * time.Hour
	minInterval     = time.Hour
	recheckNoTunnel = time.Minute
	firstBackoff    = time.Minute
)

// EventKind says what happened to a subscription.
type EventKind int

const (
	Updated EventKind = iota
	Failed
)

// Event reports an update result for the API's event stream. Code is set for
// Failed. It carries no URL.
type Event struct {
	Kind EventKind
	ID   string
	Code string
	Diff profile.Diff
}

type backoff struct {
	delay time.Duration
	next  time.Time
}

type eventBus struct {
	mu sync.Mutex
	ch chan Event
}

func newEventBus() *eventBus { return &eventBus{ch: make(chan Event, 1)} }

// publish never blocks: when the buffer is full the older event is replaced.
func (b *eventBus) publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case b.ch <- e:
		return
	default:
	}
	select {
	case <-b.ch:
	default:
	}
	b.ch <- e
}

// Events returns the latest-wins stream of update results.
func (s *Service) Events() <-chan Event { return s.events.ch }

func interval(i time.Duration) time.Duration {
	if i <= 0 {
		return defaultInterval
	}
	if i < minInterval {
		return minInterval
	}
	return i
}

// Run is the scheduler loop; it returns when ctx ends.
func (s *Service) Run(ctx context.Context) {
	for {
		s.runDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.d.Clock.After(recheckNoTunnel):
		}
	}
}

// runDue updates every subscription that is due. Without a tunnel it sends
// nothing and changes no backoff; the next Run pass re-checks.
func (s *Service) runDue(ctx context.Context) {
	s.mu.Lock()
	subs, err := s.d.Store.Load()
	s.mu.Unlock()
	if err != nil {
		return
	}
	for _, sub := range subs {
		if ctx.Err() != nil {
			return
		}
		now := s.d.Clock.Now()
		iv := interval(sub.Info.UpdateInterval)
		s.mu.Lock()
		due := sub.LastGood.Add(iv)
		st := s.sched[sub.ID]
		if st != nil {
			due = st.next
		}
		s.mu.Unlock()
		if now.Before(due) {
			continue
		}
		if s.d.Tunnel == nil {
			continue
		}
		if _, up := s.d.Tunnel.ProbeDialer(); !up {
			continue
		}
		if _, err := s.Update(ctx, sub.ID, Scheduled); err != nil {
			if codeOf(err) == CodeNotFound || codeOf(err) == CodeNoTunnel {
				continue
			}
			s.mu.Lock()
			b := s.sched[sub.ID]
			if b == nil {
				b = &backoff{}
				s.sched[sub.ID] = b
			}
			switch {
			case b.delay == 0:
				b.delay = firstBackoff
			default:
				b.delay *= 2
			}
			if b.delay > iv {
				b.delay = iv
			}
			b.next = s.d.Clock.Now().Add(b.delay)
			s.mu.Unlock()
			s.logf("subsvc: scheduled update failed (%s), retry in %s", codeOf(err), b.delay)
		}
	}
}
