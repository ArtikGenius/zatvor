package main

import (
	"os"
	"os/exec"
	"testing"
)

func git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "diff.renames=true"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A file renamed and edited in the same commit shows up as status R in the
// staged diff; its new content must still be scanned.
func TestScanStagedCoversRenameAndEdit(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	git(t, "init", "-q")
	clean := "line one\nline two\nline three\nline four\nline five\n"
	if err := os.WriteFile("old.md", []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, "add", "old.md")
	git(t, "commit", "-q", "-m", "clean")

	git(t, "mv", "old.md", "new.md")
	secret := clean + "https://panel.real-" + "host.ru:2096/sub/abc123\n"
	if err := os.WriteFile("new.md", []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, "add", "new.md")

	got, err := scanStaged(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "new.md" || got[0].Rule != "subscription-url" {
		t.Fatalf("findings = %+v", got)
	}
}

// The staged scan and the full scan must report the same findings for the
// same content: a UUID is flagged by both until it is allow-listed, and the
// allow list is honoured by both.
func TestStagedAndFullScanAgree(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	git(t, "init", "-q")
	const id = "12345678-1234-1234-1234-123456789abc"
	if err := os.WriteFile("a.txt", []byte("ok\nid "+id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, "add", "a.txt")

	for name, allow := range map[string]AllowList{"unlisted": nil, "allowed": ParseAllowList([]byte(id + " # fake\n"))} {
		staged, err := scanStaged(allow)
		if err != nil {
			t.Fatal(err)
		}
		full, err := scanPaths([]string{"."}, allow)
		if err != nil {
			t.Fatal(err)
		}
		if len(staged) != len(full) {
			t.Fatalf("%s: staged %+v != full %+v", name, staged, full)
		}
		if want := map[string]int{"unlisted": 1, "allowed": 0}[name]; len(staged) != want {
			t.Fatalf("%s: got %d findings, want %d: %+v", name, len(staged), want, staged)
		}
		for i := range staged {
			if staged[i] != full[i] {
				t.Fatalf("%s: staged %+v != full %+v", name, staged[i], full[i])
			}
		}
	}
}

// run() with --staged must exit non-zero when a staged file has a finding.
func TestRunStagedExitCode(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	git(t, "init", "-q")
	if err := os.WriteFile("a.txt", []byte("12345678-1234-1234-1234-123456789abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, "add", "a.txt")
	if got := run([]string{"--staged"}); got != 1 {
		t.Fatalf("run --staged = %d, want 1", got)
	}
}
