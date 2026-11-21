// Package sysapi holds the interfaces through which Zatvor changes the
// system. Real implementations come in later stages; in safe mode every
// interface is served by the Recorder, which changes nothing.
package sysapi

import "time"

// ProxyState is the OS system-proxy setting.
type ProxyState struct {
	Enabled bool     `json:"enabled"`
	Server  string   `json:"server,omitempty"`  // host:port
	Bypass  []string `json:"bypass,omitempty"`  // hosts that skip the proxy
	PACURL  string   `json:"pac_url,omitempty"` // auto-config URL, if any
}

// SystemProxy reads and writes the OS system proxy.
type SystemProxy interface {
	Get() (ProxyState, error)
	Set(ProxyState) error
}

// ProcID identifies a process across PID reuse: the PID plus the creation
// time exactly as the OS reports it.
type ProcID struct {
	PID     int       `json:"pid"`
	Created time.Time `json:"created"`
}

// ProcessControl freezes, unfreezes and closes processes.
type ProcessControl interface {
	Suspend(ProcID) error
	Resume(ProcID) error
	Terminate(ProcID) error
}
