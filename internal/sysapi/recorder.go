package sysapi

import (
	"encoding/json"
	"io"
	"slices"
	"sync"
	"time"
)

// Recorder implements every sysapi interface without touching the system.
// Each call that would change the system appends one JSON line
// {"ts","op","args"} to the writer; reads are answered from memory and not
// recorded, so a poll (the engine's drift check) does not grow the log.
type Recorder struct {
	mu    sync.Mutex
	w     io.Writer
	proxy ProxyState            // LAN settings
	conns map[string]ProxyState // RAS entries set through SetConn
}

// NewRecorder returns a Recorder that logs to w.
func NewRecorder(w io.Writer) *Recorder { return &Recorder{w: w} }

type entry struct {
	TS   string `json:"ts"`
	Op   string `json:"op"`
	Args any    `json:"args"`
}

// record must be called with r.mu held.
func (r *Recorder) record(op string, args any) error {
	b, err := json.Marshal(entry{TS: time.Now().UTC().Format(time.RFC3339Nano), Op: op, Args: args})
	if err != nil {
		return err
	}
	_, err = r.w.Write(append(b, '\n'))
	return err
}

func copyState(s ProxyState) ProxyState {
	s.Bypass = slices.Clone(s.Bypass)
	return s
}

// Get returns the last state passed to Set, or the zero state.
func (r *Recorder) Get() (ProxyState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return copyState(r.proxy), nil
}

// Set records the state; the OS proxy is not changed.
func (r *Recorder) Set(s ProxyState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.record("proxy.set", s); err != nil {
		return err
	}
	r.proxy = copyState(s)
	return nil
}

// Connections reports no RAS entries: the Recorder has none of its own.
func (r *Recorder) Connections() ([]string, error) { return nil, nil }

type connArgs struct {
	Conn  string      `json:"conn"`
	State *ProxyState `json:"state,omitempty"`
}

// GetConn returns the last state passed to SetConn for name, or the zero
// state. Name "" is the LAN settings, the same as Get.
func (r *Recorder) GetConn(name string) (ProxyState, error) {
	if name == "" {
		return r.Get()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return copyState(r.conns[name]), nil
}

// SetConn records the state of one connection; the OS is not changed. Name
// "" is the LAN settings, the same as Set.
func (r *Recorder) SetConn(name string, s ProxyState) error {
	if name == "" {
		return r.Set(s)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.record("proxy.set_conn", connArgs{Conn: name, State: &s}); err != nil {
		return err
	}
	if r.conns == nil {
		r.conns = map[string]ProxyState{}
	}
	r.conns[name] = copyState(s)
	return nil
}

// Suspend records the call; no process is touched.
func (r *Recorder) Suspend(id ProcID) error { return r.proc("proc.suspend", id) }

// Resume records the call; no process is touched.
func (r *Recorder) Resume(id ProcID) error { return r.proc("proc.resume", id) }

// Terminate records the call; no process is touched.
func (r *Recorder) Terminate(id ProcID) error { return r.proc("proc.terminate", id) }

func (r *Recorder) proc(op string, id ProcID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.record(op, id)
}
