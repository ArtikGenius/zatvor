package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/api"
	"github.com/ArtikGenius/zatvor/internal/appdir"
	"github.com/ArtikGenius/zatvor/internal/daemon"
	"github.com/ArtikGenius/zatvor/internal/instance"
	"github.com/ArtikGenius/zatvor/internal/journal"
	"github.com/ArtikGenius/zatvor/internal/safemode"
)

func TestZatvordPrintsSafeMode(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go tool not found: %v", err)
	}
	cmd := exec.Command(goBin, "run", ".", "--print-mode")
	cmd.Env = cleanEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "safe (build default)" {
		t.Fatalf("output = %q, want %q", got, "safe (build default)")
	}
}

// TestLiveRefusedWithoutAllowLive: the ADR 0004 gate. --live without
// ZATVOR_ALLOW_LIVE=1 resolves to safe, for --print-mode and for the run.
func TestLiveRefusedWithoutAllowLive(t *testing.T) {
	env := func(string) string { return "" }
	var out, errOut bytes.Buffer
	if code := run([]string{"--live", "--print-mode"}, env, nil, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != "safe (live refused: ZATVOR_ALLOW_LIVE=1 not set)" {
		t.Fatalf("output = %q", got)
	}
	if mode, _ := safemode.Resolve([]string{"--live"}, env); modeArg(mode) != "--safe" {
		t.Fatalf("the child of a refused --live gets %s, want --safe", modeArg(mode))
	}
}

func TestRejectsBadDataDir(t *testing.T) {
	foreign := t.TempDir()
	doc := filepath.Join(foreign, "thesis.docx")
	if err := os.WriteFile(doc, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(foreign)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"relative/dir", foreign} {
		var out, errOut bytes.Buffer
		code := run([]string{"--data-dir", dir}, func(string) string { return "" }, nil, &out, &errOut)
		if code != 2 || !strings.Contains(errOut.String(), "data directory") {
			t.Errorf("--data-dir %s: exit %d, stderr %q; want 2 and a data-directory message", dir, code, errOut.String())
		}
	}
	if _, err := os.Stat(filepath.Join(foreign, "journal")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a refused data dir got our subdirectories")
	}
	if after, _ := os.Stat(foreign); after.Mode() != before.Mode() {
		t.Fatalf("a refused data dir changed mode %v -> %v", before.Mode(), after.Mode())
	}
}

func TestChildArgs(t *testing.T) {
	got := strings.Join(childArgs(appdir.Dirs{Data: "/x/zatvor"}, "--safe"), " ")
	if got != "--supervised --data-dir /x/zatvor --safe" {
		t.Fatalf("child args = %q", got)
	}
}

// build compiles zatvord into a temp dir.
func build(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go tool not found: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "zatvord")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command(goBin, "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// fakeUserEnv points every user directory at a temp dir, without the mode
// switches.
func fakeUserEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	env := cleanEnv()
	for _, k := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_DATA_HOME", "XDG_CONFIG_HOME"} {
		env = append(env, k+"="+filepath.Join(home, strings.ToLower(k)))
	}
	return env
}

func waitFile(t *testing.T, path string, present bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := os.Stat(path)
		if (err == nil) == present {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s present=%v", path, present)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSupervisedChildStopsOnStdinClose(t *testing.T) {
	bin := build(t)
	data := filepath.Join(t.TempDir(), "zatvor")
	cmd := exec.Command(bin, "--supervised", "--data-dir", data)
	cmd.Env = fakeUserEnv(t)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	disc := filepath.Join(data, "run", api.DiscoveryFile)
	waitFile(t, disc, true)
	stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("child exit: %v\n%s", err, errOut.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the child did not stop when its stdin closed")
	}
	if _, err := os.Stat(disc); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("api.json left behind after a clean exit")
	}
	if _, err := os.Stat(filepath.Join(data, "logs", "safe-mode.jsonl")); err != nil {
		t.Fatalf("safe mode keeps its recorder log: %v", err)
	}
}

func TestKilledSupervisorStopsChild(t *testing.T) {
	// The supervisor starts itself with --supervised; when the supervisor
	// is killed the child's stdin closes and it exits cleanly.
	bin := build(t)
	data := filepath.Join(t.TempDir(), "zatvor")
	cmd := exec.Command(bin, "--data-dir", data)
	cmd.Env = fakeUserEnv(t)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	disc := filepath.Join(data, "run", api.DiscoveryFile)
	waitFile(t, disc, true)
	d, err := api.ReadDiscovery(appdir.Dirs{Run: filepath.Join(data, "run")})
	if err != nil {
		t.Fatal(err)
	}
	if d.PID == cmd.Process.Pid {
		t.Fatal("the API runs in the supervisor, want it in the --supervised child")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waitFile(t, disc, false)
}

// cleanEnv drops the mode switches so the build default shows.
func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "ZATVOR_SAFE=") || strings.HasPrefix(kv, "ZATVOR_ALLOW_LIVE=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func TestLogRotates(t *testing.T) {
	if logMax != 5<<20 {
		t.Fatalf("logMax = %d, want 5 MiB", logMax)
	}
	path := filepath.Join(t.TempDir(), "zatvord.log")
	w := newRotatingLog(path, 100)
	line := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes
	for i := 0; i < 2; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if fi, _ := os.Stat(path); fi.Size() != 80 {
		t.Fatalf("size before the limit = %d, want 80", fi.Size())
	}
	// The third line would pass 100 bytes: the file moves to .1 first.
	if _, err := w.Write(line); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Size() != 40 {
		t.Fatalf("size after rotation = %d, want 40", fi.Size())
	}
	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() != 80 {
		t.Fatalf(".1 = %v, %v; want the old 80 bytes", fi, err)
	}
	// Only one old file is kept.
	for i := 0; i < 3; i++ {
		w.Write(line)
	}
	if _, err := os.Stat(path + ".2"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a second old file was kept")
	}
	if fi, _ := os.Stat(path + ".1"); fi.Size() != 80 {
		t.Fatalf(".1 = %d bytes, want 80 (the newer full file)", fi.Size())
	}
	// A second writer (the other process) sees the same file and limit.
	other := newRotatingLog(path, 100)
	if _, err := other.Write(line); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Size() > 100 {
		t.Fatalf("the shared file grew past the limit: %d", fi.Size())
	}
}

// testLock is a lock of this test only: on Windows the mutex name is per
// user, not per data dir.
func testLock(t *testing.T, dirs appdir.Dirs) string {
	if runtime.GOOS == "windows" {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			t.Fatal(err)
		}
		return "ZatvorCmdTest-" + hex.EncodeToString(b[:])
	}
	return instance.Name(dirs)
}

func begin(t *testing.T, dir, target string) {
	t.Helper()
	j, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Begin(journal.Entry{Kind: "sysproxy", Target: target, Prev: []byte(`{}`), Applied: []byte(`{"enabled":true}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorJournalIsTheModes(t *testing.T) {
	// R16: the supervisor reverts the journal of its child's mode only.
	dirs, err := appdir.Resolve(filepath.Join(t.TempDir(), "zatvor"))
	if err != nil {
		t.Fatal(err)
	}
	begin(t, filepath.Join(dirs.Journal, "safe"), "ras:safe-entry")
	begin(t, filepath.Join(dirs.Journal, "live"), "ras:live-entry")
	opts := daemon.Options{Dirs: dirs, Mode: safemode.Safe, Env: func(string) string { return "" }}
	j, rs, closeRec, already, err := openSupervisorJournal(opts, testLock(t, dirs))
	if err != nil || already {
		t.Fatalf("open: already=%v err=%v", already, err)
	}
	defer closeRec()
	p, err := j.Pending()
	if err != nil || len(p) != 1 || p[0].Target != "ras:safe-entry" {
		t.Fatalf("pending = %+v, %v; want only the safe journal's entry", p, err)
	}
	if rs["sysproxy"] == nil || rs["devtools"] == nil {
		t.Fatalf("reverters = %v, want sysproxy and devtools", rs)
	}
}

func TestSupervisorJournalNotOpenedWhileLocked(t *testing.T) {
	// The journal of a running daemon is never opened: Open would drop its
	// torn last line while that daemon may still be appending.
	dirs, err := appdir.Resolve(filepath.Join(t.TempDir(), "zatvor"))
	if err != nil {
		t.Fatal(err)
	}
	safe := filepath.Join(dirs.Journal, "safe")
	begin(t, safe, "lan")
	f, err := os.OpenFile(filepath.Join(safe, "journal.jsonl"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"id":"torn`)
	f.Close()
	before, _ := os.ReadFile(filepath.Join(safe, "journal.jsonl"))

	name := testLock(t, dirs)
	release, held, err := instance.Acquire(name)
	if err != nil || held {
		t.Fatalf("acquire: held=%v err=%v", held, err)
	}
	defer release()
	opts := daemon.Options{Dirs: dirs, Mode: safemode.Safe, Env: func(string) string { return "" }}
	_, _, _, already, err := openSupervisorJournal(opts, name)
	if err != nil || !already {
		t.Fatalf("already=%v err=%v, want already", already, err)
	}
	if after, _ := os.ReadFile(filepath.Join(safe, "journal.jsonl")); !bytes.Equal(after, before) {
		t.Fatal("the journal of a running daemon was opened")
	}
}
