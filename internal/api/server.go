// Package api is zatvord's local HTTP API v1 (ADR 0009). It listens on
// 127.0.0.1 only, publishes its port in Run/api.json and its token in
// Run/token (both user-only), and checks the token, the Host header and the
// Origin header on every request. Every response is JSON, except the zip of
// GET /v1/diag; errors are {"code","message"} and never carry the text of a
// wrapped error.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/ArtikGenius/zatvor/internal/appdir"
	"github.com/ArtikGenius/zatvor/internal/devtools"
	"github.com/ArtikGenius/zatvor/internal/engine"
	"github.com/ArtikGenius/zatvor/internal/profile"
	"github.com/ArtikGenius/zatvor/internal/settings"
)

const (
	// Version is the API and discovery file version.
	Version = 1
	// DiscoveryFile in the Run dir holds {"version","port","pid"}.
	DiscoveryFile = "api.json"
	// TokenFile in the Run dir holds the bearer token, 64 lowercase hex.
	TokenFile = "token"
	// MaxBody caps every request body.
	MaxBody = 64 << 10
)

// Backend is what the API serves: the daemon's services, behind one
// interface so that tests use fakes. Every method is called concurrently.
// Errors that carry a code (*engine.Error, *subsvc.Error, *devtools.Error,
// *Error, also wrapped) reach the client as that code and message; any other
// error is reported as "internal".
type Backend interface {
	// Status is the current engine state.
	Status() engine.State
	// Subscribe streams events until ctx ends, then closes the channel. The
	// daemon is the only reader of the engine's and subsvc's own channels
	// and fans them out here. The fan-out must never block on a slow
	// subscriber: when a subscriber's channel is full it drops the event or
	// closes that channel (the client then reconnects and gets the current
	// state first).
	Subscribe(ctx context.Context) <-chan Event
	// Profiles are every known profile; the API strips them to ProfileInfo.
	Profiles() []profile.Profile
	// Connect connects the profile with this ID (an unknown ID is an *Error
	// with CodeNotFound).
	Connect(ctx context.Context, profileID string) error
	Disconnect(ctx context.Context) error
	ResolveConflict(ctx context.Context, force bool) error
	// Subscriptions are the stored subscriptions; the API reduces them to
	// SubscriptionInfo.
	Subscriptions() []profile.Subscription
	AddSubscription(ctx context.Context, rawURL, ua string) (profile.Subscription, profile.Diff, error)
	// UpdateSubscription is the user's update button.
	UpdateSubscription(ctx context.Context, id string) (profile.Diff, error)
	RemoveSubscription(id string) error
	// Settings are the current settings; LocalAPIPort is the port Start
	// tries first.
	Settings() settings.Settings
	// PutSettings saves s and returns what was saved.
	PutSettings(s settings.Settings) (settings.Settings, error)
	// SetDevTools turns developer-tool routing on or off.
	SetDevTools(on bool) ([]devtools.Target, error)
	// Show asks the UI to show its window (a second launch, K43).
	Show()
	// Check runs a fresh tunnel check (engine.Recheck); not connected is
	// an *engine.Error with engine.CodeNotConnected.
	Check(ctx context.Context) (engine.CheckResult, error)
	// Diag writes the diagnostics zip (diag.Bundle) to w.
	Diag(ctx context.Context, w io.Writer) error
}

// Discovery is the content of DiscoveryFile.
type Discovery struct {
	Version int `json:"version"`
	Port    int `json:"port"`
	PID     int `json:"pid"`
}

// Server is the local API server.
type Server struct {
	b    Backend
	dirs appdir.Dirs

	mu     sync.Mutex
	srv    *http.Server
	cancel context.CancelFunc
	served chan struct{}
	port   int
	token  string
}

// New returns a Server for b that publishes its files in dirs.Run.
func New(b Backend, dirs appdir.Dirs) *Server { return &Server{b: b, dirs: dirs} }

// baseKey carries the server's lifetime context in every request context.
type baseKey struct{}

// opContext is the context of a backend operation: the server's lifetime,
// not the request's, so that a client that gives up does not cancel a
// connect or an update halfway. Stop cancels it.
func opContext(r *http.Request) context.Context {
	if c, ok := r.Context().Value(baseKey{}).(context.Context); ok {
		return c
	}
	return r.Context()
}

// Start listens on 127.0.0.1 at the persisted port (Settings().LocalAPIPort)
// when it is set, is none of Xray's ports (Settings().Ports) and can be
// bound, else on a free port, then writes the token file and
// after it the discovery file, so that a reader that finds api.json always
// finds the token. It returns the bound "127.0.0.1:<port>"; persisting a new
// port is the caller's job. ctx bounds only the start itself; Stop ends the
// server.
func (s *Server) Start(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		return "", errors.New("api: already started")
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	ln, err := listen(ctx, preferredPort(s.b.Settings()))
	if err != nil {
		return "", fmt.Errorf("api: listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	tokenPath := filepath.Join(s.dirs.Run, TokenFile)
	if err := writeUserOnly(tokenPath, []byte(token)); err != nil {
		ln.Close()
		return "", fmt.Errorf("api: write token: %w", err)
	}
	disc, _ := json.Marshal(Discovery{Version: Version, Port: port, PID: os.Getpid()})
	if err := writeUserOnly(filepath.Join(s.dirs.Run, DiscoveryFile), disc); err != nil {
		ln.Close()
		os.Remove(tokenPath)
		return "", fmt.Errorf("api: write discovery: %w", err)
	}

	base, cancel := context.WithCancel(context.WithoutCancel(ctx))
	base = context.WithValue(base, baseKey{}, base)
	srv := &http.Server{
		Handler:           s.handler(addr, token),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		BaseContext:       func(net.Listener) context.Context { return base },
		// net/http logs recovered panic values and connection errors here;
		// a panic value can hold anything, so nothing is logged unscrubbed.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Serve(ln)
	}()
	s.srv, s.cancel, s.served, s.port, s.token = srv, cancel, served, port, token
	return addr, nil
}

// Stop ends every event stream and operation, closes the listener, and
// removes the discovery and token files if they are still ours (same pid and
// port, same token). It is safe to call more than once.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		return
	}
	s.cancel()
	ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	if err := s.srv.Shutdown(ctx); err != nil {
		s.srv.Close()
	}
	<-s.served
	s.removeOurs()
	s.srv, s.cancel, s.served = nil, nil, nil
}

// removeOurs deletes api.json, then the token, each only if it is ours.
func (s *Server) removeOurs() {
	if d, err := ReadDiscovery(s.dirs); err == nil && d.PID == os.Getpid() && d.Port == s.port {
		os.Remove(filepath.Join(s.dirs.Run, DiscoveryFile))
	}
	tokenPath := filepath.Join(s.dirs.Run, TokenFile)
	if b, err := os.ReadFile(tokenPath); err == nil && subtle.ConstantTimeCompare(bytes.TrimSpace(b), []byte(s.token)) == 1 {
		os.Remove(tokenPath)
	}
}

// ReadDiscovery reads and checks the discovery file in dirs.Run.
func ReadDiscovery(dirs appdir.Dirs) (Discovery, error) {
	raw, err := os.ReadFile(filepath.Join(dirs.Run, DiscoveryFile))
	if err != nil {
		return Discovery{}, fmt.Errorf("api: discovery: %w", err)
	}
	var d Discovery
	if err := json.Unmarshal(raw, &d); err != nil {
		return Discovery{}, errors.New("api: discovery file is damaged")
	}
	if d.Version != Version {
		return Discovery{}, fmt.Errorf("api: discovery version %d, want %d", d.Version, Version)
	}
	if d.Port < 1 || d.Port > 65535 {
		return Discovery{}, errors.New("api: discovery file has no valid port")
	}
	return d, nil
}

// ReadToken reads the token file in dirs.Run.
func ReadToken(dirs appdir.Dirs) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dirs.Run, TokenFile))
	if err != nil {
		return "", fmt.Errorf("api: token: %w", err)
	}
	tok := string(bytes.TrimSpace(raw))
	if len(tok) != 64 {
		return "", errors.New("api: token file is damaged")
	}
	if _, err := hex.DecodeString(tok); err != nil {
		return "", errors.New("api: token file is damaged")
	}
	return tok, nil
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("api: token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// preferredPort is the persisted local API port, or 0 when it is unset, out
// of range, or one of Xray's ports (which Xray must be able to bind).
func preferredPort(st settings.Settings) int {
	p := st.LocalAPIPort
	if p < 1 || p > 65535 {
		return 0
	}
	for _, x := range []int{st.Ports.Mixed, st.Ports.Probe, st.Ports.DevTools, st.Ports.API} {
		if p == x {
			return 0
		}
	}
	return p
}

// listen binds 127.0.0.1:port, or 127.0.0.1:0 when port is unset or taken.
func listen(ctx context.Context, port int) (net.Listener, error) {
	lc := net.ListenConfig{Control: listenControl}
	if port != 0 {
		if ln, err := lc.Listen(ctx, "tcp4", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			return ln, nil
		}
	}
	return lc.Listen(ctx, "tcp4", "127.0.0.1:0")
}

// writeUserOnly writes path atomically (temp file in the same directory,
// fsync, rename) and restricts it to the current user before the rename.
func writeUserOnly(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = appdir.RestrictToUser(tmp); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		// RestrictToUser sets 0700 (made for directories); a file needs no x bit.
		if err = os.Chmod(tmp, 0o600); err != nil {
			return err
		}
	}
	return os.Rename(tmp, path)
}
