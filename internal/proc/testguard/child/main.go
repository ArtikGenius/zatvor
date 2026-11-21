// Command child is the helper process that testguard.Spawn starts. It
// writes its handshake file, then optionally counts. It is never run by
// hand: without ZATVOR_TEST_TOKEN and ZATVOR_GUARD_DIR it exits at once.
package main

import (
	"errors"
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

	token, dir := os.Getenv("ZATVOR_TEST_TOKEN"), os.Getenv("ZATVOR_GUARD_DIR")
	if token == "" || dir == "" {
		fmt.Fprintln(os.Stderr, "child: ZATVOR_TEST_TOKEN or ZATVOR_GUARD_DIR not set; run only via testguard.Spawn")
		os.Exit(2)
	}
	if err := handshake(dir, token); err != nil {
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

// handshake writes the token to <dir>/handshake (the path
// testguard.handshakeFile builds) via a fresh temp file in dir and a rename,
// so a reader never sees a partial token.
func handshake(dir, token string) error {
	f, err := os.CreateTemp(dir, "handshake-*.tmp")
	if err != nil {
		return err
	}
	_, werr := f.WriteString(token + "\n")
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(f.Name())
		return errors.Join(werr, cerr)
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "handshake")); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}
