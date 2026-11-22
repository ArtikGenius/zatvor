// Package devtools routes developer tools through Zatvor's DevTools inbound
// (spec §9): a marked block in PowerShell and bash profiles that follows a
// state file at every prompt, two keys each in Claude Code's and VS Code's
// settings, and codex/gemini wrappers in our BinDir.
//
// The user's files are edited through symlinks with an atomic write, a
// one-time backup and a size+mtime+SHA-256 check before the rename (retried,
// never forced). Every edit is journaled first, one entry per file, and the
// journal records only our part of the file: the block, or our keys. So a
// user's other edits never count as "changed by someone else", and a revert
// takes away only what is ours.
package devtools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ArtikGenius/zatvor/internal/journal"
)

// Kind is the journal Kind of our entries.
const Kind = "devtools"

// maxAttempts is how often a file that keeps changing under us is re-read
// before we give up with CodeConflict.
const maxAttempts = 3

// Error codes.
const (
	// CodeConflict: the file changed between our read and our rename on
	// every attempt. Nothing was written; the other program's bytes stand.
	CodeConflict = "devtools_conflict"
	// CodeUnsupported: we cannot edit the file safely (JSON that does not
	// parse or is not an object, a zatvor block without its end marker, a
	// UTF-16BE profile, a broken symlink).
	CodeUnsupported = "devtools_unsupported_file"
	// CodeIO: reading, journaling or writing failed.
	CodeIO = "devtools_io"
	// CodeChangedByOther: a revert found our part of the file changed by
	// someone else and left it alone (wraps journal.ErrChangedByOther).
	CodeChangedByOther = "devtools_changed_by_other"
)

// Error is a failure on one target.
type Error struct {
	Code   string
	Target string
	Err    error
}

func (e *Error) Error() string { return "devtools: " + e.Target + ": " + e.Code + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Roots are the directories every path is derived from. StateFile holds
// the DevTools proxy URL while connected and is empty otherwise; the daemon
// writes it, the blocks and wrappers read it. BinDir holds the wrappers and
// is not put on PATH by us.
type Roots struct {
	Home, Documents, AppData, StateFile, BinDir string
}

// State of a target.
type State string

const (
	On       State = "on"       // our block, keys or wrappers are in place
	Off      State = "off"      // nothing of ours is there
	Conflict State = "conflict" // ours was changed by someone else, or we could not write
	Missing  State = "missing"  // the tool's directory does not exist
)

// Target is one tool's status. Path is the file we edit, or BinDir for the
// wrappers.
type Target struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	State State  `json:"state"`
	Note  string `json:"note,omitempty"`
}

// spec describes one user file we edit.
type spec struct {
	name string
	path func(Roots) string
	json bool
	nl   string                               // shell: newline of a new profile
	ptrs []string                             // JSON: the keys we own, in insertion order
	vals func(proxy string) map[string]string // JSON: our values
	blk  func(stateFile string) string        // shell: our block
	unit string                               // JSON: indentation step of a new file
	perm fs.FileMode                          // mode of a new file
	// policy: create a missing file only when the Windows PowerShell 5.1
	// execution policy lets profiles run (R31).
	policy bool
}

var specs = []*spec{
	{name: "pwsh5", nl: "\r\n", perm: 0o644, blk: pwshBlock, policy: true, path: func(r Roots) string {
		return under(r.Documents, "WindowsPowerShell", "Microsoft.PowerShell_profile.ps1")
	}},
	{name: "pwsh7", nl: "\r\n", perm: 0o644, blk: pwshBlock, path: func(r Roots) string {
		return under(r.Documents, "PowerShell", "Microsoft.PowerShell_profile.ps1")
	}},
	{name: "bash", nl: "\n", perm: 0o644, blk: bashBlock, path: func(r Roots) string {
		return under(r.Home, ".bashrc")
	}},
	{name: "claude", json: true, unit: "  ", perm: 0o600,
		ptrs: []string{"/env/HTTPS_PROXY", "/env/NO_PROXY"},
		vals: func(p string) map[string]string {
			return map[string]string{"/env/HTTPS_PROXY": p, "/env/NO_PROXY": noProxy}
		},
		path: func(r Roots) string { return under(r.Home, ".claude", "settings.json") }},
	{name: "vscode", json: true, unit: "    ", perm: 0o644,
		ptrs: []string{"/http.proxy", "/http.proxySupport"},
		vals: func(p string) map[string]string {
			return map[string]string{"/http.proxy": p, "/http.proxySupport": "override"}
		},
		path: func(r Roots) string { return under(r.AppData, "Code", "User", "settings.json") }},
}

// under joins elem to root; an unset root gives "" (the target is then
// Missing) rather than a path relative to the working directory.
func under(root string, elem ...string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(append([]string{root}, elem...)...)
}

func specNamed(name string) *spec {
	for _, s := range specs {
		if s.name == name {
			return s
		}
	}
	return nil
}

// current reads our part of the file; exists says whether it exists.
func (s *spec) current(data []byte, exists bool) (json.RawMessage, error) {
	if s.json {
		return newJSONDoc(data, s.unit).projection(s.ptrs, exists)
	}
	return shellCurrent(data, exists)
}

// exists reads the exists flag of a projection.
func exists(p json.RawMessage) bool {
	var v struct {
		Exists bool `json:"exists"`
	}
	return json.Unmarshal(p, &v) == nil && v.Exists
}

// ownPrev is the Prev that Enable journals. Every zatvor block is ours,
// even one the journal lost, so a profile's Prev never holds a block that
// is not journaled: such a block is recorded as "". A block that is the
// Applied of a pending entry (a second Enable, say with a new state file)
// is kept, so the new entry chains on that one and both unwind.
func (s *spec) ownPrev(prev json.RawMessage, pending []journal.Entry, key string) json.RawMessage {
	if s.json {
		return prev
	}
	for _, e := range pending {
		if e.Kind == Kind && e.Target == key && canon(e.Applied) == canon(prev) {
			return prev
		}
	}
	var p shellProjection
	if json.Unmarshal(prev, &p) != nil {
		return prev
	}
	p.Block = ""
	b, _ := json.Marshal(p)
	return b
}

// apply returns data with our part set to want (a projection).
func (s *spec) apply(data []byte, want json.RawMessage) ([]byte, error) {
	if s.json {
		var p jsonProjection
		if err := json.Unmarshal(want, &p); err != nil {
			return nil, err
		}
		d, err := newJSONDoc(data, s.unit).set(s.ptrs, p.Keys)
		if err != nil {
			return nil, err
		}
		return d.bytes(), nil
	}
	var p shellProjection
	if err := json.Unmarshal(want, &p); err != nil {
		return nil, err
	}
	return shellSet(data, p.Block, s.nl)
}

// ours is the projection Enable applies.
func (s *spec) ours(r Roots, port int) json.RawMessage {
	var p any
	if s.json {
		keys := map[string]json.RawMessage{}
		for ptr, v := range s.vals("http://127.0.0.1:" + strconv.Itoa(port)) {
			keys[ptr], _ = json.Marshal(v)
		}
		p = jsonProjection{Keys: keys, Exists: true}
	} else {
		p = shellProjection{Block: s.blk(r.StateFile), Exists: true}
	}
	b, _ := json.Marshal(p)
	return b
}

// empty reports whether a projection holds nothing.
func (s *spec) empty(cur json.RawMessage) bool {
	if s.json {
		var p jsonProjection
		if json.Unmarshal(cur, &p) != nil {
			return false
		}
		for _, v := range p.Keys {
			if string(v) != "null" {
				return false
			}
		}
		return true
	}
	var p shellProjection
	return json.Unmarshal(cur, &p) == nil && p.Block == ""
}

// projectionOf is the projection of data as the named target's existing file.
func projectionOf(name string, data []byte) (json.RawMessage, error) {
	s := specNamed(name)
	if s == nil {
		return nil, fmt.Errorf("devtools: unknown target %q", name)
	}
	return s.current(data, true)
}

// canon compares projections the way the journal does.
func canon(b []byte) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return string(b)
	}
	c, err := json.Marshal(v)
	if err != nil {
		return string(b)
	}
	return string(c)
}

func classify(target string, err error) error {
	var de *Error
	switch {
	case errors.As(err, &de):
		return err
	case errors.Is(err, ErrChanged):
		return &Error{Code: CodeConflict, Target: target, Err: err}
	case errors.Is(err, errBrokenLink):
		return &Error{Code: CodeUnsupported, Target: target, Err: err}
	}
	return &Error{Code: CodeIO, Target: target, Err: err}
}

// beginFunc journals a change before it is written and returns a function
// to call with whether the write happened.
type beginFunc func(prev, applied json.RawMessage) (finish func(written bool) error, err error)

// write makes our part of the file at path equal want. It reads, computes,
// journals (when begin is set) and writes with the Version it read; when
// the file changed meanwhile it starts over, up to maxAttempts times, and
// then returns CodeConflict having written nothing. It reports whether it
// wrote.
//
// A revert passes expect, the Applied it is undoing: every attempt first
// checks that our part still equals it, so a value the owning app wrote
// after RevertAll's check is never overwritten (CodeChangedByOther, which
// wraps journal.ErrChangedByOther). A revert to a Prev without the file
// deletes the file when what is left is exactly what creating it left.
func write(fsys FS, s *spec, path string, want, expect json.RawMessage, begin beginFunc) (bool, error) {
	for range maxAttempts {
		data, ver, err := fsys.ReadFile(path)
		if err != nil {
			return false, classify(s.name, err)
		}
		prev, err := s.current(data, ver.Exists)
		if err != nil {
			return false, &Error{Code: CodeUnsupported, Target: s.name, Err: err}
		}
		if canon(prev) == canon(want) {
			return false, nil
		}
		if expect != nil && canon(prev) != canon(expect) {
			return false, &Error{Code: CodeChangedByOther, Target: s.name, Err: fmt.Errorf("%s: %w", path, journal.ErrChangedByOther)}
		}
		out, err := s.apply(data, want)
		if err != nil {
			return false, &Error{Code: CodeUnsupported, Target: s.name, Err: err}
		}
		del := false
		if expect != nil && ver.Exists && !exists(want) {
			if created, err := s.apply(nil, expect); err == nil {
				residue, err := s.apply(created, want)
				del = err == nil && bytes.Equal(out, residue)
			}
		}
		applied, err := s.current(out, !del)
		if err != nil {
			return false, &Error{Code: CodeUnsupported, Target: s.name, Err: err}
		}
		var finish func(bool) error
		if begin != nil {
			if finish, err = begin(prev, applied); err != nil {
				return false, &Error{Code: CodeIO, Target: s.name, Err: fmt.Errorf("journal: %w", err)}
			}
		}
		err = fsys.WriteFile(path, out, WriteOpts{Perm: s.perm, Expect: ver, Backup: !del, Delete: del})
		if finish != nil {
			// Nothing was written: close the entry, there is nothing to revert.
			if ferr := finish(err == nil); ferr != nil && err != nil {
				err = errors.Join(err, ferr)
			}
		}
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, ErrChanged) {
			return false, classify(s.name, err)
		}
	}
	return false, &Error{Code: CodeConflict, Target: s.name, Err: fmt.Errorf("%s kept changing during %d attempts; nothing was written", path, maxAttempts)}
}

// policyAllows reports whether a Windows PowerShell execution policy runs
// a local, unsigned profile.
func policyAllows(p string) bool {
	switch strings.ToLower(p) {
	case "remotesigned", "unrestricted", "bypass":
		return true
	}
	return false
}

// Manager enables and disables developer-tool routing.
type Manager struct {
	roots Roots
	j     *journal.Journal
	fs    FS
	paths map[string]string // spec name → absolute path
	// policy returns the effective Windows PowerShell 5.1 execution
	// policy, or "" where there is none (not Windows).
	policy func() string

	mu        sync.Mutex
	conflicts map[string]string      // target → why the last Enable could not write it
	last      []journal.RevertResult // devtools results of the last Disable
}

// New returns a Manager over the files under roots. j journals every change
// of a user file; fs is OSFS, or a Recorder in safe mode.
func New(roots Roots, j *journal.Journal, fsys FS) *Manager {
	m := &Manager{roots: roots, j: j, fs: fsys, paths: map[string]string{}, conflicts: map[string]string{}, policy: executionPolicy}
	for _, s := range specs {
		p := s.path(roots)
		if a, err := filepath.Abs(p); err == nil && p != "" {
			p = a
		}
		m.paths[s.name] = p
	}
	return m
}

func targetKey(name, path string) string { return name + ":" + path }

// Enable routes every installed tool through 127.0.0.1:port: it journals and
// writes the profile blocks and settings keys, and writes the wrappers. A
// tool whose directory does not exist is skipped (Status: Missing), and so
// is a missing Windows PowerShell 5.1 profile under an execution policy that
// would not run it. The error joins one *Error per target that failed; the
// others are enabled. Enabling again with another port adds a second entry
// per file; the revert goes newest first, so both unwind.
func (m *Manager) Enable(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("devtools: invalid port %d", port)
	}
	if !filepath.IsAbs(m.roots.StateFile) || !filepath.IsAbs(m.roots.BinDir) {
		return errors.New("devtools: StateFile and BinDir must be absolute paths")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []error
	for _, s := range specs {
		path := m.paths[s.name]
		if path == "" {
			continue
		}
		_, ver, err := m.fs.ReadFile(path)
		if err == nil && !ver.Exists && (!m.fs.DirExists(filepath.Dir(path)) || s.policy && !policyAllows(m.policy())) {
			delete(m.conflicts, s.name)
			continue // not installed (never create the tool's directory), or a profile that would not run
		}
		key := targetKey(s.name, path)
		begin := func(prev, applied json.RawMessage) (func(bool) error, error) {
			pending, err := m.j.Pending()
			if err != nil {
				return nil, err
			}
			id, err := m.j.Begin(journal.Entry{Kind: Kind, Target: key, Prev: s.ownPrev(prev, pending, key), Applied: applied})
			if err != nil {
				return nil, err
			}
			return func(written bool) error {
				if written {
					return nil
				}
				return m.j.Done(id)
			}, nil
		}
		wrote, err := write(m.fs, s, path, s.ours(m.roots, port), nil, begin)
		if err == nil && !wrote && !s.json {
			err = m.adopt(s, key, path)
		}
		if err != nil {
			m.conflicts[s.name] = err.Error()
			errs = append(errs, err)
			continue
		}
		delete(m.conflicts, s.name)
	}
	if err := m.writeWrappers(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// adopt journals a profile that already holds exactly our block but has no
// pending entry (the journal was lost): every zatvor block is ours, so
// Disable must remove it.
func (m *Manager) adopt(s *spec, key, path string) error {
	pending, err := m.j.Pending()
	if err != nil {
		return &Error{Code: CodeIO, Target: s.name, Err: err}
	}
	for _, e := range pending {
		if e.Kind == Kind && e.Target == key {
			return nil
		}
	}
	data, ver, err := m.fs.ReadFile(path)
	if err != nil {
		return classify(s.name, err)
	}
	cur, err := s.current(data, ver.Exists)
	if err != nil || s.empty(cur) {
		return nil
	}
	if _, err := m.j.Begin(journal.Entry{Kind: Kind, Target: key, Prev: s.ownPrev(cur, nil, key), Applied: cur}); err != nil {
		return &Error{Code: CodeIO, Target: s.name, Err: fmt.Errorf("journal: %w", err)}
	}
	return nil
}

func (m *Manager) writeWrappers() error {
	if err := m.fs.MkdirAll(m.roots.BinDir); err != nil {
		return &Error{Code: CodeIO, Target: "wrappers", Err: err}
	}
	var errs []error
	for _, tool := range wrappedTools {
		for _, f := range wrapperFiles(tool, m.roots.BinDir, m.roots.StateFile) {
			path := filepath.Join(m.roots.BinDir, f.name)
			data, ver, err := m.fs.ReadFile(path)
			if err == nil && ver.Exists && string(data) == f.body {
				continue
			}
			if err == nil {
				err = m.fs.WriteFile(path, []byte(f.body), WriteOpts{Perm: f.perm, Expect: ver})
			}
			if err != nil {
				errs = append(errs, classify(tool, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Disable reverts every pending devtools journal entry whose file still
// holds what we wrote; ours that someone changed is left to them (see
// LastRevert). Then it deletes the wrappers. The error joins the failures.
func (m *Manager) Disable() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var (
		errs []error
		ours []journal.RevertResult
	)
	for _, r := range m.j.RevertAll(map[string]journal.Reverter{Kind: m.Reverter()}) {
		if r.Kind != Kind && r.Kind != "" { // "" is a journal read failure
			continue
		}
		ours = append(ours, r)
		if r.Outcome == journal.Failed {
			name, _, _ := strings.Cut(r.Target, ":")
			errs = append(errs, classify(name, r.Err))
		}
	}
	m.last = ours
	for _, tool := range wrappedTools {
		for _, f := range wrapperFiles(tool, m.roots.BinDir, m.roots.StateFile) {
			if err := m.fs.Remove(filepath.Join(m.roots.BinDir, f.name)); err != nil {
				errs = append(errs, classify(tool, err))
			}
		}
	}
	clear(m.conflicts)
	return errors.Join(errs...)
}

// LastRevert returns the devtools results of the last Disable, so the
// caller can warn about files left to the program that changed them.
func (m *Manager) LastRevert() []journal.RevertResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]journal.RevertResult(nil), m.last...)
}

// Status reports every known target: the five files, then codex and gemini.
func (m *Manager) Status() []Target {
	m.mu.Lock()
	defer m.mu.Unlock()
	applied := map[string]json.RawMessage{} // target key → Applied of the newest pending entry
	pending, jerr := m.j.Pending()
	for _, e := range pending {
		if e.Kind == Kind {
			applied[e.Target] = e.Applied
		}
	}
	var out []Target
	for _, s := range specs {
		path := m.paths[s.name]
		t := Target{Name: s.name, Path: path}
		t.State, t.Note = m.fileState(s, path, applied, jerr)
		if why, ok := m.conflicts[s.name]; ok {
			t.State, t.Note = Conflict, why
		}
		out = append(out, t)
	}
	for _, tool := range wrappedTools {
		t := Target{Name: tool, Path: m.roots.BinDir, State: m.wrapperState(tool)}
		if cfg := under(m.roots.Home, ".codex", "config.toml"); tool == "codex" && cfg != "" {
			if data, ver, err := m.fs.ReadFile(cfg); err == nil && ver.Exists && respectsSystemProxy(data) {
				t.Note = cfg + " sets respect_system_proxy = true: Codex then follows the system proxy and ignores HTTPS_PROXY from this wrapper"
			}
		}
		out = append(out, t)
	}
	return out
}

func (m *Manager) fileState(s *spec, path string, applied map[string]json.RawMessage, jerr error) (State, string) {
	if path == "" {
		return Missing, "its base directory is not known"
	}
	data, ver, err := m.fs.ReadFile(path)
	if err != nil {
		return Conflict, "cannot read the file: " + err.Error()
	}
	if !ver.Exists && !m.fs.DirExists(filepath.Dir(path)) {
		return Missing, filepath.Dir(path) + " does not exist"
	}
	if !ver.Exists && s.policy {
		if p := m.policy(); p == "" {
			return Missing, "Windows PowerShell 5.1 is not available here"
		} else if !policyAllows(p) {
			return Missing, "execution policy prevents profiles (" + p + "); an existing profile would still be edited"
		}
	}
	cur, err := s.current(data, ver.Exists)
	if err != nil {
		return Conflict, "cannot edit the file safely: " + err.Error()
	}
	if jerr != nil {
		return Conflict, "cannot read the journal: " + jerr.Error()
	}
	if a, ok := applied[targetKey(s.name, path)]; ok {
		if canon(cur) == canon(a) {
			if !s.json && !blockIsLast(data) {
				return On, "the Zatvor block is not the last thing in the profile; a prompt function defined after it turns the routing off"
			}
			return On, ""
		}
		return Conflict, "changed by another program after Zatvor set it; Disable will leave it as it is"
	}
	switch {
	case s.empty(cur):
		return Off, ""
	case s.json:
		return Off, "the file already sets a proxy; Enable replaces it and Disable puts it back"
	}
	return Conflict, "holds a Zatvor block that is not journaled; Enable takes it over and Disable removes it"
}

func (m *Manager) wrapperState(tool string) State {
	files := wrapperFiles(tool, m.roots.BinDir, m.roots.StateFile)
	var same, present int
	for _, f := range files {
		data, ver, err := m.fs.ReadFile(filepath.Join(m.roots.BinDir, f.name))
		if err != nil || !ver.Exists {
			continue
		}
		present++
		if string(data) == f.body {
			same++
		}
	}
	switch {
	case same == len(files):
		return On
	case present == 0:
		return Off
	}
	return Conflict
}

// respectsSystemProxy scans config.toml for `respect_system_proxy = true`
// line by line, without a TOML parser.
func respectsSystemProxy(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.Trim(strings.TrimSpace(k), `"'`) == "respect_system_proxy" && strings.TrimSpace(v) == "true" {
			return true
		}
	}
	return false
}

// Reverter returns the journal.Reverter for Kind. It works from the entry's
// target alone ("<name>:<absolute path>"), so a later process can revert
// what an earlier one wrote.
func (m *Manager) Reverter() journal.Reverter { return reverter{fs: m.fs} }

type reverter struct{ fs FS }

func parseTarget(target string) (*spec, string, error) {
	name, path, ok := strings.Cut(target, ":")
	s := specNamed(name)
	if !ok || s == nil || !filepath.IsAbs(path) {
		return nil, "", fmt.Errorf("devtools: unknown target %q", target)
	}
	return s, path, nil
}

func (r reverter) Current(target string) (json.RawMessage, error) {
	s, path, err := parseTarget(target)
	if err != nil {
		return nil, err
	}
	data, ver, err := r.fs.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return s.current(data, ver.Exists)
}

// Restore puts our part of the file back to prev and keeps every other
// byte. RevertAll calls RestoreIf instead.
func (r reverter) Restore(target string, prev json.RawMessage) error {
	return r.RestoreIf(target, nil, prev)
}

// RestoreIf is Restore that, at every attempt, first checks that our part
// still equals applied (journal.ConditionalRestorer).
func (r reverter) RestoreIf(target string, applied, prev json.RawMessage) error {
	s, path, err := parseTarget(target)
	if err != nil {
		return err
	}
	_, err = write(r.fs, s, path, prev, applied, nil)
	return err
}
