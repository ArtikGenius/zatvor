//go:build systemtest

// Package systest gates system tests: tests that really change the host
// (freeze processes, set the system proxy, ...). They run only on
// disposable CI runners, never on a developer machine.
//
// Every systemtest package (build tag systemtest) has
//
//	func TestMain(m *testing.M) { systest.Main(m) }
//
// and calls systest.Require(t) first in every test. Both refuse unless env
// CI is exactly "true" (GitHub Actions sets it) and ZATVOR_ALLOW_LIVE is
// exactly "1" (only the CI systemtest job sets it).
package systest

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Refusal is the message printed (and wrapped in errors) when the gate refuses.
const Refusal = "systemtest runs only on CI runners"

// ExitRefused is the exit code of a systemtest binary that the gate refused.
const ExitRefused = 2

// check returns nil only if env says this is a CI runner with live mode allowed.
func check(env func(string) string) error {
	var missing []string
	if env("CI") != "true" {
		missing = append(missing, `env CI is not "true"`)
	}
	if env("ZATVOR_ALLOW_LIVE") != "1" {
		missing = append(missing, `env ZATVOR_ALLOW_LIVE is not "1"`)
	}
	if missing != nil {
		return fmt.Errorf("%s (%s)", Refusal, strings.Join(missing, "; "))
	}
	return nil
}

// Main is the TestMain of a systemtest package: it exits 2 with Refusal on
// stderr unless the gate allows, and otherwise runs the tests.
func Main(m *testing.M) {
	if err := check(os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(ExitRefused)
	}
	fmt.Fprintf(os.Stderr, "systest: live system tests on runner %q\n", runnerName())
	os.Exit(m.Run())
}

// Require fails the test at once unless the gate allows, and logs the
// runner name. Call it first in every systemtest.
func Require(t testing.TB) {
	t.Helper()
	if err := check(os.Getenv); err != nil {
		t.Fatal(err)
	}
	t.Logf("systest: runner %q", runnerName())
}

func runnerName() string {
	if n := os.Getenv("RUNNER_NAME"); n != "" {
		return n
	}
	return "(RUNNER_NAME unset)"
}
