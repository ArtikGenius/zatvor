package journal

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeRev struct {
	vals      map[string]json.RawMessage
	writes    []string
	restoreEr error
}

func newFake() *fakeRev { return &fakeRev{vals: map[string]json.RawMessage{}} }

func (f *fakeRev) Current(t string) (json.RawMessage, error) { return f.vals[t], nil }
func (f *fakeRev) Restore(t string, p json.RawMessage) error {
	if f.restoreEr != nil {
		return f.restoreEr
	}
	f.vals[t] = p
	f.writes = append(f.writes, t)
	return nil
}

func open(t *testing.T) (*Journal, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "j")
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return j, dir
}

func entry(kind, target, prev, applied string) Entry {
	return Entry{Kind: kind, Target: target, Prev: json.RawMessage(prev), Applied: json.RawMessage(applied), At: time.Unix(1700000000, 0).UTC()}
}

func TestRevertRestoresPrev(t *testing.T) {
	j, _ := open(t)
	f := newFake()
	f.vals["p"] = json.RawMessage(`{"a": 2}`) // whitespace differs from Applied
	id, err := j.Begin(entry("k", "p", `{"a":1}`, `{"a":2}`))
	if err != nil || id == "" {
		t.Fatal(id, err)
	}
	res := j.RevertAll(map[string]Reverter{"k": f})
	if len(res) != 1 || res[0].Outcome != Restored || res[0].Err != nil {
		t.Fatalf("%+v", res)
	}
	if string(f.vals["p"]) != `{"a":1}` {
		t.Fatalf("got %s", f.vals["p"])
	}
	if p, _ := j.Pending(); len(p) != 0 {
		t.Fatalf("pending %v", p)
	}
}

func TestRevertSkipsChangedByOther(t *testing.T) {
	j, _ := open(t)
	f := newFake()
	f.vals["p"] = json.RawMessage(`{"a":3}`)
	_, _ = j.Begin(entry("k", "p", `{"a":1}`, `{"a":2}`))
	res := j.RevertAll(map[string]Reverter{"k": f})
	if len(res) != 1 || res[0].Outcome != SkippedChangedByOther {
		t.Fatalf("%+v", res)
	}
	if len(f.writes) != 0 || string(f.vals["p"]) != `{"a":3}` {
		t.Fatal("wrote over foreign value")
	}
	if p, _ := j.Pending(); len(p) != 0 {
		t.Fatal("entry must be closed")
	}
}

func TestRevertNewestFirst(t *testing.T) {
	j, _ := open(t)
	f := newFake()
	f.vals["p"] = json.RawMessage(`"c"`)
	_, _ = j.Begin(entry("k", "p", `"a"`, `"b"`))
	_, _ = j.Begin(entry("k", "p", `"b"`, `"c"`))
	res := j.RevertAll(map[string]Reverter{"k": f})
	if len(res) != 2 || res[0].Outcome != Restored || res[1].Outcome != Restored {
		t.Fatalf("%+v", res)
	}
	if string(f.vals["p"]) != `"a"` {
		t.Fatalf("got %s", f.vals["p"])
	}
}

func TestTornLastLine(t *testing.T) {
	j, dir := open(t)
	f := newFake()
	f.vals["p"] = json.RawMessage(`2`)
	_, _ = j.Begin(entry("k", "p", `1`, `2`))
	fh, _ := os.OpenFile(filepath.Join(dir, "journal.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = fh.WriteString(`{"id":"zz","kind":"k","tar`)
	fh.Close()

	j2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := j2.Pending()
	if err != nil || len(p) != 1 {
		t.Fatal(p, err)
	}
	// A new append after the torn tail must not be glued to it.
	if _, err := j2.Begin(entry("k", "q", `1`, `2`)); err != nil {
		t.Fatal(err)
	}
	if p, _ := j2.Pending(); len(p) != 2 {
		t.Fatalf("pending %v", p)
	}
	res := j2.RevertAll(map[string]Reverter{"k": f})
	if len(res) != 2 || string(f.vals["p"]) != `1` {
		t.Fatalf("%+v %v", res, f.vals)
	}
}

func TestBeginFsyncsBeforeReturn(t *testing.T) {
	j, _ := open(t)
	var fileSync, dirSync int
	j.syncFile = func(*os.File) error { fileSync++; return nil }
	j.syncDir = func(string) error { dirSync++; return nil }
	if _, err := j.Begin(entry("k", "p", `1`, `2`)); err != nil {
		t.Fatal(err)
	}
	if fileSync != 1 || dirSync != 1 {
		t.Fatalf("file=%d dir=%d", fileSync, dirSync)
	}
}

func TestDoneIsIdempotent(t *testing.T) {
	j, _ := open(t)
	id, _ := j.Begin(entry("k", "p", `1`, `2`))
	if err := j.Done(id); err != nil {
		t.Fatal(err)
	}
	if err := j.Done(id); err != nil {
		t.Fatal(err)
	}
	if err := j.Done("unknown"); err != nil {
		t.Fatal(err)
	}
	if p, _ := j.Pending(); len(p) != 0 {
		t.Fatal(p)
	}
}

func TestCompaction(t *testing.T) {
	j, dir := open(t)
	f := newFake()
	rs := map[string]Reverter{"k": f}
	for i := 0; i < 1000; i++ {
		f.vals["p"] = json.RawMessage(`2`)
		if _, err := j.Begin(entry("k", "p", `1`, `2`)); err != nil {
			t.Fatal(err)
		}
		j.RevertAll(rs)
		st, err := os.Stat(filepath.Join(dir, "journal.jsonl"))
		if err != nil || st.Size() != 0 {
			t.Fatalf("cycle %d size %v err %v", i, st, err)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftover files: %v", ents)
	}
}

func TestFailedAndMissingReverterStayPending(t *testing.T) {
	j, dir := open(t)
	f := newFake()
	f.vals["p"] = json.RawMessage(`2`)
	f.restoreEr = errors.New("boom")
	_, _ = j.Begin(entry("k", "p", `1`, `2`))
	_, _ = j.Begin(entry("other", "x", `1`, `2`))
	res := j.RevertAll(map[string]Reverter{"k": f})
	if len(res) != 2 || res[0].Outcome != Failed || res[1].Outcome != Failed {
		t.Fatalf("%+v", res)
	}
	if !errors.Is(res[1].Err, f.restoreEr) || !errors.Is(res[0].Err, ErrNoReverter) {
		t.Fatalf("%v %v", res[0].Err, res[1].Err)
	}
	if p, _ := j.Pending(); len(p) != 2 {
		t.Fatalf("pending %v", p)
	}
	if st, _ := os.Stat(filepath.Join(dir, "journal.jsonl")); st.Size() == 0 {
		t.Fatal("must not compact while pending")
	}
	// A later start with working reverters closes them.
	f.restoreEr = nil
	f.vals["x"] = json.RawMessage(`2`)
	res = j.RevertAll(map[string]Reverter{"k": f, "other": f})
	if res[0].Outcome != Restored || res[1].Outcome != Restored {
		t.Fatalf("%+v", res)
	}
}
