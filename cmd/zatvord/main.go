// Command zatvord is the Zatvor daemon. In stage 0c it only reports its
// version and mode; it changes nothing on the system.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ArtikGenius/zatvor/internal/safemode"
)

const version = "0.0.0-dev"

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }

func run(args []string, env func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("zatvord", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Bool("safe", false, "safe mode: change nothing on the system")
	printMode := fs.Bool("print-mode", false, "print the resolved mode and its source, then exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "zatvord: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	switch {
	case *showVersion:
		fmt.Fprintln(stdout, version)
	case *printMode:
		mode, src := safemode.Resolve(args, env)
		fmt.Fprintf(stdout, "%s (%s)\n", mode, src)
	default:
		fmt.Fprintln(stderr, "zatvord: nothing to run yet; use --version or --print-mode")
		return 2
	}
	return 0
}
