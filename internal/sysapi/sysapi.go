// Package sysapi holds the interfaces through which Zatvor changes the
// system. Real implementations come in later stages; in safe mode every
// interface is served by the Recorder, which changes nothing.
package sysapi

import "time"

// ProxyState is the OS system-proxy setting of one connection.
type ProxyState struct {
	Enabled    bool     `json:"enabled"`               // manual proxy (Server) in use
	Server     string   `json:"server,omitempty"`      // host:port, kept even when not Enabled
	Bypass     []string `json:"bypass,omitempty"`      // hosts that skip the proxy, in order
	PACURL     string   `json:"pac_url,omitempty"`     // auto-config URL in use, if any
	AutoDetect bool     `json:"auto_detect,omitempty"` // WPAD auto-detection in use
}

// SystemProxy reads and writes the OS system proxy. Get and Set are the LAN
// settings; GetConn and SetConn address one connection, where name "" is the
// LAN settings and any other name is an entry listed by Connections.
type SystemProxy interface {
	Get() (ProxyState, error)
	Set(ProxyState) error
	// Connections lists the dial-up/VPN (RAS) entries, not the LAN.
	Connections() ([]string, error)
	GetConn(name string) (ProxyState, error)
	SetConn(name string, s ProxyState) error
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
