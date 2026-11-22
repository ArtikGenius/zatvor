package api

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/ArtikGenius/zatvor/internal/devtools"
	"github.com/ArtikGenius/zatvor/internal/engine"
	"github.com/ArtikGenius/zatvor/internal/profile"
	"github.com/ArtikGenius/zatvor/internal/settings"
	"github.com/ArtikGenius/zatvor/internal/subsvc"
)

// API error codes of the API itself; domain errors keep their own codes.
const (
	CodeForbidden     = "forbidden"
	CodeBadRequest    = "bad_request"
	CodeNotFound      = "not_found"
	CodeBodyTooLarge  = "body_too_large"
	CodeSettingsEmpty = "settings_empty"
	CodeInternal      = "internal"
)

// Error is an API error with a stable code and an English developer
// message. A Backend returns it for failures that have no domain error, such
// as an unknown profile ID (CodeNotFound).
type Error struct{ Code, Message string }

func (e *Error) Error() string { return "api: " + e.Code + ": " + e.Message }

// errorJSON is the body of every error response.
type errorJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Status codes by error code. Codes not listed are 422: the request was
// understood but the operation failed.
var codeStatus = map[string]int{
	CodeForbidden:         http.StatusForbidden,
	CodeBadRequest:        http.StatusBadRequest,
	CodeNotFound:          http.StatusNotFound,
	CodeBodyTooLarge:      http.StatusRequestEntityTooLarge,
	CodeSettingsEmpty:     http.StatusBadRequest,
	CodeInternal:          http.StatusInternalServerError,
	subsvc.CodeInvalidURL: http.StatusBadRequest,
	// subsvc.CodeNotFound is "not_found" as well.
	engine.CodeProxyConflict:    http.StatusConflict,
	engine.CodeNoConflict:       http.StatusConflict,
	engine.CodeNotConnected:     http.StatusConflict,
	engine.CodeSuperseded:       http.StatusConflict,
	devtools.CodeConflict:       http.StatusConflict,
	devtools.CodeChangedByOther: http.StatusConflict,
}

// errorOf maps err to its response. Only the code and message of a typed
// error are used, never err.Error() of the chain, which can hold a URL or a
// path.
func errorOf(err error) (int, errorJSON) {
	var body errorJSON
	var (
		ae *Error
		ee *engine.Error
		se *subsvc.Error
		de *devtools.Error
		mb *http.MaxBytesError
	)
	switch {
	case errors.As(err, &ae):
		body = errorJSON{ae.Code, ae.Message}
	case errors.As(err, &ee):
		body = errorJSON{ee.Code, ee.Message}
	case errors.As(err, &se):
		body = errorJSON{se.Code, se.Message}
	case errors.As(err, &de):
		body = errorJSON{de.Code, "devtools: " + de.Target + ": " + de.Code}
	case errors.As(err, &mb):
		body = errorJSON{CodeBodyTooLarge, fmt.Sprintf("request body over %d bytes", MaxBody)}
	case errors.Is(err, settings.ErrRefuseEmpty):
		body = errorJSON{CodeSettingsEmpty, "refusing to overwrite the settings with empty ones"}
	default:
		body = errorJSON{CodeInternal, "internal error"}
	}
	if body.Code == "" {
		body = errorJSON{CodeInternal, "internal error"}
	}
	st, ok := codeStatus[body.Code]
	if !ok {
		st = http.StatusUnprocessableEntity
	}
	return st, body
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		b, _ = json.Marshal(errorJSON{CodeInternal, "internal error"})
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	w.Write(append(b, '\n'))
}

func writeError(w http.ResponseWriter, err error) {
	st, body := errorOf(err)
	writeJSON(w, st, body)
}

func badRequest(msg string) error { return &Error{CodeBadRequest, msg} }

// decode reads exactly one JSON value from the (size-capped) body.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	err := dec.Decode(v)
	if err == nil {
		if err = dec.Decode(&struct{}{}); err == io.EOF {
			return nil
		} else if err == nil {
			return badRequest("more than one JSON value in the body")
		}
	}
	var mb *http.MaxBytesError
	if errors.As(err, &mb) {
		return err
	}
	return badRequest("invalid JSON body")
}

// handler is the whole API: the request checks, then the routes.
func (s *Server) handler(host, token string) http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, &Error{CodeNotFound, "no such endpoint"})
	})
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed(r, host, want) {
			writeJSON(w, http.StatusForbidden, errorJSON{CodeForbidden, CodeForbidden})
			return
		}
		// ServeMux would answer an unclean path with a text/html redirect.
		if path.Clean(r.URL.Path) != r.URL.Path {
			writeError(w, &Error{CodeNotFound, "no such endpoint"})
			return
		}
		if r.ContentLength > MaxBody {
			writeError(w, &http.MaxBytesError{Limit: MaxBody})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
		mux.ServeHTTP(w, r)
	})
}

// allowed checks, in this order: Host is exactly our "127.0.0.1:<port>"
// (DNS rebinding), Origin is absent, empty or "null" (cross-origin pages;
// 1c adds the UI's own origin), and the bearer token matches in constant
// time.
func allowed(r *http.Request, host string, want []byte) bool {
	if r.Host != host {
		return false
	}
	switch o := r.Header.Values("Origin"); {
	case len(o) > 1:
		return false
	case len(o) == 1 && o[0] != "" && o[0] != "null":
		return false
	}
	auth := r.Header.Values("Authorization")
	return len(auth) == 1 && subtle.ConstantTimeCompare([]byte(auth[0]), want) == 1
}

// routes registers every /v1 endpoint.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/status", s.status)
	mux.HandleFunc("GET /v1/events", s.events)
	mux.HandleFunc("GET /v1/profiles", s.profiles)
	mux.HandleFunc("POST /v1/connect", s.connect)
	mux.HandleFunc("POST /v1/disconnect", s.disconnect)
	mux.HandleFunc("GET /v1/subscriptions", s.subscriptions)
	mux.HandleFunc("POST /v1/subscriptions", s.addSubscription)
	mux.HandleFunc("POST /v1/subscriptions/{id}/update", s.updateSubscription)
	mux.HandleFunc("DELETE /v1/subscriptions/{id}", s.removeSubscription)
	mux.HandleFunc("PUT /v1/settings", s.putSettings)
	mux.HandleFunc("POST /v1/devtools", s.devtools)
	mux.HandleFunc("POST /v1/proxy/resolve-conflict", s.resolveConflict)
	mux.HandleFunc("POST /v1/show", s.show)
	mux.HandleFunc("POST /v1/check", s.check)
	mux.HandleFunc("GET /v1/diag", s.diag)
}

var okJSON = struct {
	OK bool `json:"ok"`
}{true}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.b.Status())
}

func (s *Server) profiles(w http.ResponseWriter, _ *http.Request) {
	ps := s.b.Profiles()
	out := make([]ProfileInfo, 0, len(ps))
	for _, p := range ps {
		out = append(out, profileInfo(p))
	}
	writeJSON(w, http.StatusOK, struct {
		Profiles []ProfileInfo `json:"profiles"`
	}{out})
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile string `json:"profile"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.Profile == "" {
		writeError(w, badRequest("profile is required"))
		return
	}
	if err := s.b.Connect(opContext(r), req.Profile); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.b.Status())
}

func (s *Server) disconnect(w http.ResponseWriter, r *http.Request) {
	if err := s.b.Disconnect(opContext(r)); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.b.Status())
}

func (s *Server) resolveConflict(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Force *bool `json:"force"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.Force == nil {
		writeError(w, badRequest("force is required"))
		return
	}
	if err := s.b.ResolveConflict(opContext(r), *req.Force); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.b.Status())
}

func (s *Server) subscriptions(w http.ResponseWriter, _ *http.Request) {
	subs := s.b.Subscriptions()
	out := make([]SubscriptionInfo, 0, len(subs))
	for _, sub := range subs {
		out = append(out, subscriptionInfo(sub))
	}
	writeJSON(w, http.StatusOK, struct {
		Subscriptions []SubscriptionInfo `json:"subscriptions"`
	}{out})
}

func (s *Server) addSubscription(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
		UA  string `json:"ua"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.URL == "" {
		writeError(w, badRequest("url is required"))
		return
	}
	sub, diff, err := s.b.AddSubscription(opContext(r), req.URL, req.UA)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Subscription SubscriptionInfo `json:"subscription"`
		Diff         diffJSON         `json:"diff"`
	}{subscriptionInfo(sub), diffOf(diff)})
}

func (s *Server) updateSubscription(w http.ResponseWriter, r *http.Request) {
	diff, err := s.b.UpdateSubscription(opContext(r), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Diff diffJSON `json:"diff"`
	}{diffOf(diff)})
}

func (s *Server) removeSubscription(w http.ResponseWriter, r *http.Request) {
	if err := s.b.RemoveSubscription(r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okJSON)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var req settings.Settings
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	saved, err := s.b.PutSettings(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) devtools(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On *bool `json:"on"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.On == nil {
		writeError(w, badRequest("on is required"))
		return
	}
	targets, err := s.b.SetDevTools(*req.On)
	if err != nil {
		writeError(w, err)
		return
	}
	if targets == nil {
		targets = []devtools.Target{}
	}
	writeJSON(w, http.StatusOK, struct {
		Targets []devtools.Target `json:"targets"`
	}{targets})
}

func (s *Server) show(w http.ResponseWriter, _ *http.Request) {
	s.b.Show()
	writeJSON(w, http.StatusOK, okJSON)
}

// check answers with the engine.CheckResult of a fresh tunnel check.
func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	res, err := s.b.Check(opContext(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// diag answers with the diagnostics zip, the one response that is not
// JSON. The zip is built in memory first, so a failure at any point is
// still a JSON error and never a cut-off zip.
func (s *Server) diag(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	if err := s.b.Diag(r.Context(), &buf); err != nil {
		writeError(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="zatvor-diag.zip"`)
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}

// ProfileInfo is a profile as the API lists it: no server address, no
// credentials, no link.
type ProfileInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Subscription string `json:"subscription"`
	Kind         string `json:"kind"`               // "link" or "json"
	Protocol     string `json:"protocol,omitempty"` // the link scheme
	Routing      string `json:"routing"`            // the routing source kind
	Disabled     bool   `json:"disabled"`
}

func profileInfo(p profile.Profile) ProfileInfo {
	pi := ProfileInfo{ID: p.ID, Name: p.Name, Subscription: p.SubscriptionID, Kind: "json",
		Routing: string(p.RoutingSource.Kind), Disabled: p.Disabled}
	if pi.Routing == "" {
		pi.Routing = string(profile.SourceProvider)
	}
	if p.Node != nil {
		pi.Kind, pi.Protocol = "link", p.Node.Scheme
	}
	return pi
}

// SubscriptionInfo is a subscription as the API lists it: the URL reduced to
// its host, no profiles (only their count), no provider links.
type SubscriptionInfo struct {
	ID               string     `json:"id"`
	Host             string     `json:"host"`
	Title            string     `json:"title"`
	UA               string     `json:"ua"`
	SendDevice       bool       `json:"sendDevice"`
	Upload           int64      `json:"upload"`
	Download         int64      `json:"download"`
	Total            int64      `json:"total"`
	Expire           *time.Time `json:"expire,omitempty"`
	UpdateInterval   int64      `json:"updateIntervalSec"`
	Announce         string     `json:"announce,omitempty"`
	HWIDNotSupported bool       `json:"hwidNotSupported"`
	HWIDLimitReached bool       `json:"hwidLimitReached"`
	LastGood         *time.Time `json:"lastGood,omitempty"`
	Profiles         int        `json:"profiles"`
}

func subscriptionInfo(s profile.Subscription) SubscriptionInfo {
	si := SubscriptionInfo{
		ID: s.ID, Host: profile.URLHost(s.URL), Title: s.Title, UA: s.UA, SendDevice: s.SendDevice,
		Upload: s.Info.Upload, Download: s.Info.Download, Total: s.Info.Total,
		UpdateInterval: int64(s.Info.UpdateInterval / time.Second), Announce: s.Info.Announce,
		HWIDNotSupported: s.Info.HWIDNotSupported, HWIDLimitReached: s.Info.HWIDLimitReached,
		Profiles: len(s.Profiles),
	}
	if !s.Info.Expire.IsZero() {
		t := s.Info.Expire
		si.Expire = &t
	}
	if !s.LastGood.IsZero() {
		t := s.LastGood
		si.LastGood = &t
	}
	return si
}

// diffJSON is profile.Diff with lower-case keys and no null lists.
type diffJSON struct {
	Added    []string `json:"added"`
	Removed  []string `json:"removed"`
	Changed  []string `json:"changed"`
	Warnings []string `json:"warnings"`
}

func diffOf(d profile.Diff) diffJSON {
	nz := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	return diffJSON{nz(d.Added), nz(d.Removed), nz(d.Changed), nz(d.Warnings)}
}
