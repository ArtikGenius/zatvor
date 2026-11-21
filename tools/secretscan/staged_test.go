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
