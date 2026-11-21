//go:build systemtest

package systest

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// gateChildEnv marks a re-exec of this test binary whose only job is to run
// Main and show whether the gate refuses.
const gateChildEnv = "ZATVOR_SYSTEST_GATE_CHILD"

// TestMain runs the gate only in the re-exec child. This package changes
// nothing on the host, so its own tests run anywhere: that is what lets
// TestSystemtestRefusesOutsideCI pass on a developer machine.
func TestMain(m *testing.M) {
	if os.Getenv(gateChildEnv) == "1" {
		Main(m)
		return
	}
	os.Exit(m.Run())
}

func TestSystemtestRefusesOutsideCI(t *testing.T) {
	cases := []struct {
		name string
		env  []string // added after removing CI and ZATVOR_ALLOW_LIVE
	}{
		// ZATVOR_ALLOW_LIVE keeps whatever value this process has, so on the
		// CI runner the refusal comes from CI alone.
		{"CI unset", inherit("ZATVOR_ALLOW_LIVE")},
		{"CI=false", append(inherit("ZATVOR_ALLOW_LIVE"), "CI=false")},
		{"ZATVOR_ALLOW_LIVE unset", []string{"CI=true"}},
		{"ZATVOR_ALLOW_LIVE=0", []string{"CI=true", "ZATVOR_ALLOW_LIVE=0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := without(without(os.Environ(), "CI"), "ZATVOR_ALLOW_LIVE")
			env = append(env, tc.env...)
			env = append(env, gateChildEnv+"=1")
			// -test.run=^$ : even if the gate let it through, run no test.
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Env = env
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 2 {
				t.Fatalf("re-exec err = %v, want exit code 2; stderr:\n%s", err, stderr.String())
			}
			if !strings.Contains(stderr.String(), Refusal) {
				t.Fatalf("stderr %q does not contain %q", stderr.String(), Refusal)
			}
			t.Logf("refused with exit 2: %s", strings.TrimSpace(stderr.String()))
		})
	}
}

func TestAllowed(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{}, false},
		{map[string]string{"CI": "true"}, false},
		{map[string]string{"ZATVOR_ALLOW_LIVE": "1"}, false},
		{map[string]string{"CI": "1", "ZATVOR_ALLOW_LIVE": "1"}, false},
		{map[string]string{"CI": "TRUE", "ZATVOR_ALLOW_LIVE": "1"}, false},
		{map[string]string{"CI": "true", "ZATVOR_ALLOW_LIVE": "true"}, false},
		{map[string]string{"CI": "true", "ZATVOR_ALLOW_LIVE": " 1"}, false},
		{map[string]string{"CI": "true", "ZATVOR_ALLOW_LIVE": "1"}, true},
	}
	for _, tc := range cases {
		err := check(func(k string) string { return tc.env[k] })
		if got := err == nil; got != tc.want {
			t.Errorf("check(%v) = %v, want allowed=%v", tc.env, err, tc.want)
		}
		if err != nil && !strings.Contains(err.Error(), Refusal) {
			t.Errorf("check(%v) error %q lacks %q", tc.env, err, Refusal)
		}
	}
}

func inherit(key string) []string {
	if v, ok := os.LookupEnv(key); ok {
		return []string{key + "=" + v}
	}
	return nil
}

func without(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}
