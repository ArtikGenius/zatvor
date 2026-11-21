package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
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
