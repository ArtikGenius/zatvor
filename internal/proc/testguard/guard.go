// Package testguard lets tests act only on processes they spawned.
//
// Spawn starts the helper program testguard/child with a fresh random token
// in env ZATVOR_TEST_TOKEN and a fresh private directory (t.TempDir) in env
// ZATVOR_GUARD_DIR. The child writes the token to <dir>/handshake (temp file
// + rename). Check accepts a ProcID only if all hold:
//
//   - this test process spawned it (in-process registry),
//   - the process is running (not exited, not a zombie),
//   - its OS creation time equals id.Created exactly (guards PID reuse),
//   - its parent is this test process,
//   - the handshake file in the directory Spawn recorded for it holds the
//     token Spawn generated.
//
// Any failure, including failure to read OS data, is a refusal. Another
// process's environment is never read; the handshake file is used on every OS.
//
// The child is built with "go build" into the test's temp dir (the go build
// cache makes repeat builds cheap). Re-executing the test binary was rejected:
// a package that forgot to wire a TestMain hook would recursively run its
// whole test suite in the "child".
package testguard

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/sysapi"
)

// ProcID is sysapi.ProcID.
type ProcID = sysapi.ProcID

// TokenEnv is the env var that carries the per-child token.
const TokenEnv = "ZATVOR_TEST_TOKEN"

// DirEnv is the env var that names the child's private handshake directory.
const DirEnv = "ZATVOR_GUARD_DIR"

const childPkg = "github.com/ArtikGenius/zatvor/internal/proc/testguard/child"

// handshakeTimeout bounds how long Spawn waits for the child's handshake.
const handshakeTimeout = 30 * time.Second

// osInfo is what the OS reports about a running process.
type osInfo struct {
	ppid    int
	created time.Time
}

type child struct {
	token    string
	dir      string // private handshake directory
	cmd      *exec.Cmd
	stdin    io.Closer
	done     chan struct{} // closed after cmd.Wait returns
	stderr   *bytes.Buffer // read only after done
	stopOnce sync.Once
}

var (
	mu       sync.Mutex
	registry = map[int]*child{}
)

// handshakeFile is the file in dir where the child writes its token.
// testguard/child builds the same path.
func handshakeFile(dir string) string { return filepath.Join(dir, "handshake") }

// Spawn starts the helper named name (only "child", the program in
// testguard/child, exists) with args, waits for its handshake and returns its
// identity. The child is killed and its handshake file removed in t.Cleanup.
// testguard/child flags: -counter <path> writes an incrementing counter to
// path every 50 ms.
func Spawn(t *testing.T, name string, args ...string) ProcID {
	t.Helper()
	if name != "child" {
		t.Fatalf("testguard.Spawn: unknown helper %q (only \"child\")", name)
	}
	bin := buildChild(t)

	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		t.Fatalf("testguard.Spawn: token: %v", err)
	}
	token := hex.EncodeToString(tok)

	dir := t.TempDir() // private, removed after the cleanup below has run

	cmd := exec.Command(bin, args...)
	cmd.Env = append(envWithout(envWithout(os.Environ(), TokenEnv), DirEnv),
		TokenEnv+"="+token, DirEnv+"="+dir)
	stdin, err := cmd.StdinPipe() // child exits on EOF, i.e. when we die
	if err != nil {
		t.Fatalf("testguard.Spawn: %v", err)
	}
	c := &child{token: token, dir: dir, cmd: cmd, stdin: stdin, done: make(chan struct{}), stderr: new(bytes.Buffer)}
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("testguard.Spawn: start: %v", err)
	}
	go func() { _ = cmd.Wait(); close(c.done) }()
	pid := cmd.Process.Pid

	mu.Lock()
	registry[pid] = c
	mu.Unlock()
	t.Cleanup(func() {
		c.stop()
		mu.Lock()
		if registry[pid] == c {
			delete(registry, pid)
		}
		mu.Unlock()
	})

	info, err := procInfo(pid)
	if err != nil {
		t.Fatalf("testguard.Spawn: read own child %d: %v", pid, err)
	}
	if err := waitHandshake(c); err != nil {
		t.Fatalf("testguard.Spawn: %v", err)
	}
	return ProcID{PID: pid, Created: info.created}
}

// Check returns nil only if id is a running child spawned by this test
// process with Spawn; see the package doc for the exact conditions.
func Check(id ProcID) error {
	if id.PID <= 0 {
		return fmt.Errorf("testguard: refusing pid %d: not a valid pid", id.PID)
	}
	if id.PID == os.Getpid() {
		return fmt.Errorf("testguard: refusing pid %d: it is the test process itself", id.PID)
	}
	mu.Lock()
	c := registry[id.PID]
	mu.Unlock()
	if c == nil {
		return fmt.Errorf("testguard: refusing pid %d: not spawned by this test process", id.PID)
	}
	return verify(id, c.token, c.dir)
}

// verify checks id against the OS and the handshake file in dir.
func verify(id ProcID, token, dir string) error {
	info, err := procInfo(id.PID)
	if err != nil {
		return fmt.Errorf("testguard: refusing pid %d: %w", id.PID, err)
	}
	if !info.created.Equal(id.Created) {
		return fmt.Errorf("testguard: refusing pid %d: creation time %s differs from expected %s (pid reused?)",
			id.PID, info.created.Format(time.RFC3339Nano), id.Created.Format(time.RFC3339Nano))
	}
	if self := os.Getpid(); info.ppid != self {
		return fmt.Errorf("testguard: refusing pid %d: parent is %d, not this test process %d", id.PID, info.ppid, self)
	}
	b, err := os.ReadFile(handshakeFile(dir))
	if err != nil {
		return fmt.Errorf("testguard: refusing pid %d: handshake: %w", id.PID, err)
	}
	got := strings.TrimSpace(string(b))
	if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
		return fmt.Errorf("testguard: refusing pid %d: handshake token does not match", id.PID)
	}
	return nil
}

// stop kills the child of id and waits for it to exit. The registry entry
// stays until cleanup, so a later Check exercises the OS checks.
func stop(t *testing.T, id ProcID) {
	t.Helper()
	mu.Lock()
	c := registry[id.PID]
	mu.Unlock()
	if c == nil {
		t.Fatalf("testguard: stop: pid %d not spawned here", id.PID)
	}
	c.stop()
}

func (c *child) stop() {
	c.stopOnce.Do(func() {
		_ = c.stdin.Close()
		_ = c.cmd.Process.Kill()
		<-c.done
	})
}

func waitHandshake(c *child) error {
	pid := c.cmd.Process.Pid
	deadline := time.After(handshakeTimeout)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if b, err := os.ReadFile(handshakeFile(c.dir)); err == nil && strings.TrimSpace(string(b)) == c.token {
			return nil
		}
		select {
		case <-c.done:
			return fmt.Errorf("child %d exited before handshake: %s", pid, strings.TrimSpace(c.stderr.String()))
		case <-deadline:
			return fmt.Errorf("child %d: no handshake in %s", pid, handshakeTimeout)
		case <-tick.C:
		}
	}
}

func buildChild(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("testguard: go tool not found: %v", err)
	}
	out := filepath.Join(t.TempDir(), "zatvor-guard-child")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command(goBin, "build", "-o", out, childPkg)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("testguard: build child: %v\n%s", err, b)
	}
	return out
}

func envWithout(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

var errExited = errors.New("process has exited")
