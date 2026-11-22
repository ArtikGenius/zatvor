// Package journal records system changes before they are applied, so that a
// crash or kill never leaves the user's proxy or files in our state (spec §8).
// The file is JSON lines: an entry line, or {"done":id} closing an entry.
package journal

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Entry describes one change: what we touched, its previous value and the
// value we wrote.
type Entry struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Target  string          `json:"target"`
	Prev    json.RawMessage `json:"prev"`
	Applied json.RawMessage `json:"applied"`
	At      time.Time       `json:"at"`
}

// Reverter reads and restores the state of targets of one Kind.
type Reverter interface {
	Current(target string) (json.RawMessage, error)
	Restore(target string, prev json.RawMessage) error
}

type Outcome string

const (
	Restored              Outcome = "restored"
	SkippedChangedByOther Outcome = "skipped_changed_by_other"
	Failed                Outcome = "failed"
)

// RevertResult reports what happened to one entry.
type RevertResult struct {
	Entry
	Outcome Outcome
	Err     error
}

// ErrNoReverter is the Err of a Failed result whose Kind has no Reverter.
var ErrNoReverter = errors.New("journal: no reverter for kind")

const fileName = "journal.jsonl"

type line struct {
	Entry
	Done string `json:"done,omitempty"`
}

// Journal is safe for concurrent use by one process.
type Journal struct {
	mu   sync.Mutex
	dir  string
	path string

	syncFile func(*os.File) error
	syncDir  func(string) error
}

// Open creates dir (0700) if needed and drops a torn final line, if any.
func Open(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	j := &Journal{dir: dir, path: filepath.Join(dir, fileName), syncFile: (*os.File).Sync, syncDir: syncDir}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.truncateTorn(); err != nil {
		return nil, err
	}
	return j, nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	err = d.Sync()
	if runtime.GOOS == "windows" {
		return nil // directories cannot be fsynced there; rename/create are durable
	}
	return err
}

// truncateTorn cuts the file after the last newline so a crash mid-append
// cannot glue the next line to a fragment.
func (j *Journal) truncateTorn() error {
	b, err := os.ReadFile(j.path)
	if errors.Is(err, os.ErrNotExist) || len(b) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	if b[len(b)-1] == '\n' {
		return nil
	}
	return os.Truncate(j.path, int64(bytes.LastIndexByte(b, '\n')+1))
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (j *Journal) append(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := j.syncFile(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Begin appends the entry and fsyncs it, then returns its ID. Call it before
// applying the change. An empty e.ID gets a random one.
func (j *Journal) Begin(e Entry) (string, error) {
	if e.ID == "" {
		id, err := newID()
		if err != nil {
			return "", err
		}
		e.ID = id
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.append(line{Entry: e}); err != nil {
		return "", err
	}
	// Synced on every Begin: the file may have just been created or renamed.
	if err := j.syncDir(j.dir); err != nil {
		return "", err
	}
	return e.ID, nil
}

// Done marks an entry reverted. Unknown or already closed IDs are a no-op.
func (j *Journal) Done(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.done(id)
}

func (j *Journal) done(id string) error {
	p, err := j.pending()
	if err != nil {
		return err
	}
	for _, e := range p {
		if e.ID == id {
			return j.append(line{Done: id})
		}
	}
	return nil
}

// Pending returns the open entries, oldest first. A torn final line is ignored.
func (j *Journal) Pending() ([]Entry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.pending()
}

func (j *Journal) pending() ([]Entry, error) {
	b, err := os.ReadFile(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	closed := map[string]bool{}
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break // torn final line
		}
		raw := bytes.TrimSpace(b[:i])
		b = b[i+1:]
		if len(raw) == 0 {
			continue
		}
		var l line
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		switch {
		case l.Done != "":
			closed[l.Done] = true
		case l.ID != "":
			out = append(out, l.Entry)
		}
	}
	live := out[:0]
	for _, e := range out {
		if !closed[e.ID] {
			live = append(live, e)
		}
	}
	return live, nil
}

func canon(b json.RawMessage) []byte {
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return b
	}
	var v any
	if json.Unmarshal(buf.Bytes(), &v) != nil {
		return buf.Bytes()
	}
	c, err := json.Marshal(v) // sorted keys
	if err != nil {
		return buf.Bytes()
	}
	return c
}

// RevertAll reverts pending entries newest first, keyed by Entry.Kind.
// Prev is restored only if the current value still equals Applied; otherwise
// the value belongs to someone else and the entry is closed untouched. Failed
// entries (including a missing Reverter) stay pending. When nothing is
// pending afterwards the file is compacted to empty.
func (j *Journal) RevertAll(rs map[string]Reverter) []RevertResult {
	j.mu.Lock()
	defer j.mu.Unlock()
	p, err := j.pending()
	if err != nil {
		return []RevertResult{{Outcome: Failed, Err: err}}
	}
	var res []RevertResult
	for i := len(p) - 1; i >= 0; i-- {
		e := p[i]
		r := RevertResult{Entry: e}
		rv, ok := rs[e.Kind]
		if !ok || rv == nil {
			r.Outcome, r.Err = Failed, fmt.Errorf("%w %q", ErrNoReverter, e.Kind)
			res = append(res, r)
			continue
		}
		cur, err := rv.Current(e.Target)
		if err != nil {
			r.Outcome, r.Err = Failed, err
			res = append(res, r)
			continue
		}
		if !bytes.Equal(canon(cur), canon(e.Applied)) {
			r.Outcome = SkippedChangedByOther
		} else if err := rv.Restore(e.Target, e.Prev); err != nil {
			r.Outcome, r.Err = Failed, err
			res = append(res, r)
			continue
		} else {
			r.Outcome = Restored
		}
		if err := j.done(e.ID); err != nil {
			r.Outcome, r.Err = Failed, err
		}
		res = append(res, r)
	}
	if rest, err := j.pending(); err == nil && len(rest) == 0 {
		_ = j.compact() // housekeeping: a failure leaves only closed lines
	}
	return res
}

// compact atomically replaces the file with an empty one.
func (j *Journal) compact() error {
	if st, err := os.Stat(j.path); err != nil || st.Size() == 0 {
		return err
	}
	tmp, err := os.CreateTemp(j.dir, "journal-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := j.syncFile(tmp); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, j.path); err != nil {
		os.Remove(name)
		return err
	}
	return j.syncDir(j.dir)
}
