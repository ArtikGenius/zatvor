// Command secretscan fails when files contain what looks like a real secret:
// a subscription URL, an unlisted UUID, a private key, or (in testdata/) a
// public IPv4 address. Known-fake values are listed in allow.txt.
//
// Usage: go run ./tools/secretscan [--staged] [paths...]
package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const allowFile = "tools/secretscan/allow.txt"

var skipDirs = map[string]bool{".git": true, ".superpowers": true, "node_modules": true, "target": true, "brief": true}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	staged := false
	var paths []string
	for _, a := range args {
		if a == "--staged" {
			staged = true
		} else {
			paths = append(paths, a)
		}
	}

	var allow AllowList
	if data, err := os.ReadFile(allowFile); err == nil {
		allow = ParseAllowList(data)
	}

	var findings []Finding
	var err error
	if staged {
		findings, err = scanStaged(allow)
	} else {
		if len(paths) == 0 {
			paths = []string{"."}
		}
		findings, err = scanPaths(paths, allow)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "secretscan:", err)
		return 2
	}
	for _, f := range findings {
		fmt.Printf("%s:%d: %s\n", f.Path, f.Line, f.Rule)
	}
	if len(findings) > 0 {
		fmt.Fprintf(os.Stderr, "secretscan: %d finding(s); fix them or, if the value is fake/public, add it to %s with a comment\n", len(findings), allowFile)
		return 1
	}
	return 0
}

func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}

func scanFile(path string, content []byte, allow AllowList) []Finding {
	if isBinary(content) {
		return nil
	}
	return Scan(filepath.ToSlash(path), content, allow)
}

func scanPaths(paths []string, allow AllowList) ([]Finding, error) {
	var out []Finding
	for _, root := range paths {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != root && skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || filepath.ToSlash(filepath.Clean(p)) == allowFile {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out = append(out, scanFile(filepath.Clean(p), data, allow)...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// scanStaged scans the staged version of each added/changed file, not the
// working tree, so unstaged edits cannot hide a staged secret.
func scanStaged(allow AllowList) ([]Finding, error) {
	list, err := exec.Command("git", "diff", "--cached", "--name-only", "--no-renames", "--diff-filter=ACMR", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --cached: %w", err)
	}
	var out []Finding
	for _, name := range strings.Split(string(list), "\x00") {
		if name == "" || name == allowFile {
			continue
		}
		data, err := exec.Command("git", "show", ":"+name).Output()
		if err != nil {
			return nil, fmt.Errorf("git show :%s: %w", name, err)
		}
		out = append(out, scanFile(name, data, allow)...)
	}
	return out, nil
}
