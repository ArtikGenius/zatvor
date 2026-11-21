// Command child is the helper process that testguard.Spawn starts. It
// writes its handshake file, then optionally counts. It is never run by
// hand: without ZATVOR_TEST_TOKEN it exits at once.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// maxLife is a backstop in case the parent cannot kill us.
const maxLife = 10 * time.Minute

func main() {
	counter := flag.String("counter", "", "write an incrementing counter to this file every 50 ms")
	flag.Parse()

	token := os.Getenv("ZATVOR_TEST_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "child: ZATVOR_TEST_TOKEN not set; run only via testguard.Spawn")
		os.Exit(2)
	}
	if err := handshake(token); err != nil {
		fmt.Fprintln(os.Stderr, "child: handshake:", err)
		os.Exit(1)
	}

	// Exit when the parent closes stdin or dies.
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}()

	deadline := time.Now().Add(maxLife)
	for n := 1; time.Now().Before(deadline); n++ {
		if *counter != "" {
			if err := os.WriteFile(*counter, []byte(strconv.Itoa(n)+"\n"), 0o600); err != nil {
				fmt.Fprintln(os.Stderr, "child: counter:", err)
				os.Exit(1)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// handshake writes the token to <os.TempDir()>/zatvor-guard-<pid> (same path
// as testguard.HandshakePath) via a temp file and rename, so a reader never
// sees a partial token.
func handshake(token string) error {
	path := filepath.Join(os.TempDir(), "zatvor-guard-"+strconv.Itoa(os.Getpid()))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
