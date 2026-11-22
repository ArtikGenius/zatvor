package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ArtikGenius/zatvor/internal/engine"
	"github.com/ArtikGenius/zatvor/internal/subsvc"
)

// EventKind is the SSE event name.
type EventKind string

const (
	EventState        EventKind = "state"
	EventSubscription EventKind = "subscription"
	EventWarning      EventKind = "warning"
)

// Event is one entry of GET /v1/events. Only the field of its Kind is used.
type Event struct {
	Kind         EventKind
	State        engine.State
	Subscription subsvc.Event
	Warning      engine.Error
}

// StateEvent reports an engine state.
func StateEvent(s engine.State) Event { return Event{Kind: EventState, State: s} }

// SubscriptionEvent reports an update result of subsvc.
func SubscriptionEvent(e subsvc.Event) Event {
	return Event{Kind: EventSubscription, Subscription: e}
}

// WarningEvent reports a warning (an engine warning code and message).
func WarningEvent(w engine.Error) Event { return Event{Kind: EventWarning, Warning: w} }

// subscriptionJSON is the data of a subscription event.
type subscriptionJSON struct {
	ID     string   `json:"id"`
	Result string   `json:"result"` // "updated" or "failed"
	Code   string   `json:"code,omitempty"`
	Diff   diffJSON `json:"diff"`
}

func (e Event) data() any {
	switch e.Kind {
	case EventSubscription:
		res := "updated"
		if e.Subscription.Kind == subsvc.Failed {
			res = "failed"
		}
		return subscriptionJSON{e.Subscription.ID, res, e.Subscription.Code, diffOf(e.Subscription.Diff)}
	case EventWarning:
		return errorJSON{e.Warning.Code, e.Warning.Message}
	default:
		return e.State
	}
}

func writeEvent(w io.Writer, e Event) error {
	b, err := json.Marshal(e.data())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Kind, b)
	return err
}

// keepAlive is the interval of the comment lines that keep idle streams
// open through clients with read timeouts.
var keepAlive = 25 * time.Second

// events streams Server-Sent Events: the current state first, then every
// event of the backend until the client leaves or the server stops.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ch := s.b.Subscribe(ctx)
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	// Every write gets its own deadline, set right before it (never before a
	// wait), so a client that stops reading ends its stream instead of
	// holding this handler forever, while an idle stream stays open.
	deadline := func() { _ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)) }
	deadline()
	w.WriteHeader(http.StatusOK)
	if writeEvent(w, StateEvent(s.b.Status())) != nil || rc.Flush() != nil {
		return
	}
	tick := time.NewTicker(keepAlive)
	defer tick.Stop()
	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			deadline()
			err = writeEvent(w, ev)
		case <-tick.C:
			deadline()
			_, err = io.WriteString(w, ": keep-alive\n\n")
		}
		if err != nil || rc.Flush() != nil {
			return
		}
	}
}

// sseWriteTimeout bounds each write to an event stream.
var sseWriteTimeout = 10 * time.Second
