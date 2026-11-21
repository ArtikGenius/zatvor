package testguard

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGuardAcceptsOwnChild(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "counter")
	id := Spawn(t, "child", "-counter", counter)
	if id.PID <= 0 || id.Created.IsZero() {
		t.Fatalf("bad ProcID %+v", id)
	}
	if err := Check(id); err != nil {
		t.Fatalf("Check(own child) = %v, want nil", err)
	}
	// The child is really running: its counter advances.
	first := readCounter(t, counter, 5*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for readCounter(t, counter, time.Second) <= first {
		if time.Now().After(deadline) {
			t.Fatal("child counter does not advance")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGuardRefusesForeignPID(t *testing.T) {
	ppid := os.Getppid()
	info, infoErr := procInfo(ppid)
	// With infoErr == nil this is the correct identity of a real process
	// that we did not spawn.
	id := ProcID{PID: ppid, Created: info.created}
	err := Check(id)
	if err == nil {
		t.Fatal("Check(parent of test process) = nil, want refusal")
	}
	t.Logf("Check(parent) refused: %v", err)
	if infoErr != nil {
		t.Logf("parent %d not readable (%v); parent-check part skipped", ppid, infoErr)
	} else if err := verify(id, "token", t.TempDir()); err == nil || !strings.Contains(err.Error(), "parent") {
		// Even with a token on record, the parent check alone refuses it.
		t.Fatalf("verify(parent) = %v, want parent refusal", err)
	} else {
		t.Logf("verify(parent) refused: %v", err)
	}
	if err := Check(ProcID{PID: os.Getpid(), Created: time.Now()}); err == nil {
		t.Fatal("Check(self) = nil, want refusal")
	}
	if err := Check(ProcID{}); err == nil {
		t.Fatal("Check(zero) = nil, want refusal")
	}
}

func TestGuardRefusesReusedPID(t *testing.T) {
	id := Spawn(t, "child")
	shifted := id
	shifted.Created = id.Created.Add(time.Second)
	err := Check(shifted)
	if err == nil || !strings.Contains(err.Error(), "creation time") {
		t.Fatalf("Check(created+1s) = %v, want creation time refusal", err)
	}
	t.Logf("refused: %v", err)
	if err := Check(id); err != nil {
		t.Fatalf("Check(id) after refusal = %v, want nil", err)
	}
}

func TestGuardRefusesExited(t *testing.T) {
	id := Spawn(t, "child")
	if err := Check(id); err != nil {
		t.Fatalf("Check before exit = %v", err)
	}
	stop(t, id)
	err := Check(id)
	if err == nil {
		t.Fatal("Check(exited child) = nil, want refusal")
	}
	t.Logf("refused: %v", err)
}

func TestGuardRefusesWrongToken(t *testing.T) {
	id := Spawn(t, "child")
	if err := verify(id, "not-the-token", registered(t, id).dir); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("verify(wrong token) = %v, want token refusal", err)
	}
}

func TestGuardRefusesMissingHandshake(t *testing.T) {
	id := Spawn(t, "child")
	if err := Check(id); err != nil {
		t.Fatalf("Check before delete = %v", err)
	}
	if err := os.Remove(handshakeFile(registered(t, id).dir)); err != nil {
		t.Fatal(err)
	}
	err := Check(id)
	if err == nil || !strings.Contains(err.Error(), "handshake") {
		t.Fatalf("Check(live child, handshake deleted) = %v, want handshake refusal", err)
	}
	t.Logf("refused: %v", err)
}

func TestHandshakeIsInPerSpawnDir(t *testing.T) {
	id := Spawn(t, "child")
	dir := registered(t, id).dir
	if filepath.Clean(dir) == filepath.Clean(os.TempDir()) {
		t.Fatalf("handshake dir %s is the shared temp dir", dir)
	}
	if _, err := os.Stat(filepath.Join(os.TempDir(), "zatvor-guard-"+strconv.Itoa(id.PID))); err == nil {
		t.Fatal("handshake file written to the shared temp dir")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "handshake" {
		t.Fatalf("handshake dir holds %v, want only handshake", entries)
	}
}

func registered(t *testing.T, id ProcID) *child {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	c := registry[id.PID]
	if c == nil {
		t.Fatalf("pid %d not registered", id.PID)
	}
	return c
}

func readCounter(t *testing.T, path string, wait time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		b, err := os.ReadFile(path)
		if err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return n
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no counter in %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
