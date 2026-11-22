package sysapi

import (
	"encoding/json"
	"io"
	"slices"
	"sync"
	"time"
)

// Recorder implements every sysapi interface without touching the system.
// Each call appends one JSON line {"ts","op","args"} to the writer.
type Recorder struct {
	mu    sync.Mutex
	w     io.Writer
	proxy ProxyState
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
	if err := r.record("proxy.get", struct{}{}); err != nil {
		return ProxyState{}, err
	}
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
