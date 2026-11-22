// Command zatvord is the Zatvor daemon.
//
// Without --supervised it is the supervisor: it starts itself again with
// --supervised and restarts that child after crashes (backoff 1..16 s);
// when the child crashes 5 times within 2 minutes it reverts the change
// journal so the user's system proxy comes back (spec §8). The child runs
// the daemon until its stdin closes, which is how the supervisor stops it
// (and why a killed supervisor takes its child down cleanly).
//
// Safe mode is the default; live mode needs --live (or ZATVOR_SAFE=0)
// together with ZATVOR_ALLOW_LIVE=1, and Windows (ADR 0004).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/ArtikGenius/zatvor/internal/appdir"
	"github.com/ArtikGenius/zatvor/internal/daemon"
	"github.com/ArtikGenius/zatvor/internal/instance"
	"github.com/ArtikGenius/zatvor/internal/rotlog"
	"github.com/ArtikGenius/zatvor/internal/safemode"
	"github.com/ArtikGenius/zatvor/internal/supervisor"
)

const version = "0.0.0-dev"

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, env func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("zatvord", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Bool("safe", false, "safe mode: change nothing on the system (the default)")
	fs.Bool("live", false, "live mode: change the system; needs ZATVOR_ALLOW_LIVE=1 and Windows")
	printMode := fs.Bool("print-mode", false, "print the resolved mode and its source, then exit")
	supervised := fs.Bool("supervised", false, "run the daemon itself (the supervisor's child) until stdin closes")
	dataDir := fs.String("data-dir", "", "the data directory (default: the per-user directory of this OS)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "zatvord: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	// The ADR 0004 gate, the same for --print-mode and the run.
	mode, src := safemode.Resolve(args, env)
	switch {
	case *showVersion:
		fmt.Fprintln(stdout, version)
		return 0
	case *printMode:
		fmt.Fprintf(stdout, "%s (%s)\n", mode, src)
		return 0
	}
	if src == safemode.SourceLiveRefused {
		fmt.Fprintln(stderr, "zatvord: live mode refused (ZATVOR_ALLOW_LIVE=1 is not set); running in safe mode")
	}
	if mode == safemode.Live && runtime.GOOS != "windows" {
		fmt.Fprintln(stderr, "zatvord: live mode exists on Windows only")
		return 2
	}

	base := *dataDir
	if base != "" {
		if err := appdir.ValidateDataDir(base, appdir.Home(env)); err != nil {
			fmt.Fprintf(stderr, "zatvord: --data-dir: %v\n", err)
			return 2
		}
	} else {
		var err error
		if base, err = appdir.Default(env); err != nil {
			fmt.Fprintf(stderr, "zatvord: %v\n", err)
			return 1
		}
	}
	dirs, err := appdir.Resolve(base)
	if err != nil {
		fmt.Fprintf(stderr, "zatvord: %v\n", err)
		return 1
	}
	logw := teeLog{rotlog.New(filepath.Join(dirs.Logs, daemon.LogFile), rotlog.Max), stderr}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "zatvord: %v\n", err)
		return 1
	}
	exeDir := filepath.Dir(exe)
	xray := "xray"
	if runtime.GOOS == "windows" {
		xray += ".exe"
	}
	opts := daemon.Options{
		Dirs:       dirs,
		Mode:       mode,
		XrayPath:   filepath.Join(exeDir, xray),
		BundledGeo: filepath.Join(exeDir, "geo"),
		Version:    version,
		Env:        env,
		Log:        logw,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *supervised {
		return runChild(ctx, opts, stdin, stderr)
	}
	return runSupervisor(ctx, opts, exe, stdout, stderr)
}

// modeArg is the flag that gives the child the mode this process resolved.
func modeArg(m safemode.Mode) string {
	if m == safemode.Live {
		return "--live"
	}
	return "--safe"
}

func childArgs(dirs appdir.Dirs, mode string) []string {
	return []string{"--supervised", "--data-dir", dirs.Data, mode}
}

// openSupervisorJournal opens the journal of opts.Mode (R16) and its
// reverters while holding the instance lock name, so journal.Open (which
// drops a torn last line) never runs while another daemon owns that
// journal. already is true, with nothing opened, when another instance
// holds the lock. The lock is released before returning: the child takes
// it.
func openSupervisorJournal(opts daemon.Options, name string) (*daemon.RevertSet, bool, error) {
	release, already, err := instance.Acquire(name)
	if err != nil {
		return nil, false, err
	}
	if already {
		return nil, true, nil
	}
	defer release()
	rv, err := daemon.Reverters(opts)
	if err != nil {
		return nil, false, err
	}
	return rv, false, nil
}

// runChild runs the daemon until a signal or until stdin closes.
func runChild(ctx context.Context, opts daemon.Options, stdin io.Reader, stderr io.Writer) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if stdin != nil {
		go func() {
			_, _ = io.Copy(io.Discard, stdin)
			cancel()
		}()
	}
	if err := daemon.Run(ctx, opts); err != nil {
		fmt.Fprintf(stderr, "zatvord: %v\n", err)
		return 1
	}
	return 0
}

// runSupervisor starts the daemon as its own --supervised child. A second
// launch while a daemon runs only forwards to it.
func runSupervisor(ctx context.Context, opts daemon.Options, exe string, stdout, stderr io.Writer) int {
	name := instance.Name(opts.Dirs)
	rv, already, err := openSupervisorJournal(opts, name)
	if err != nil {
		fmt.Fprintf(stderr, "zatvord: %v\n", err)
		return 1
	}
	if already {
		if err := daemon.Forward(ctx, opts.Dirs); err != nil {
			fmt.Fprintf(stderr, "zatvord: %v\n", err)
			return 1
		}
		return 0
	}
	args := childArgs(opts.Dirs, modeArg(opts.Mode))
	child := func() *exec.Cmd {
		cmd := exec.Command(exe, args...)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		return cmd
	}
	logf := func(format string, a ...any) { fmt.Fprintf(opts.Log, "zatvord: "+format+"\n", a...) }
	lock := func() (func(), bool) {
		rel, held, err := instance.Acquire(name)
		if err != nil || held {
			return func() {}, false
		}
		return rel, true
	}
	// A give-up (*supervisor.GaveUpError) has already reverted the journal
	// and emptied the dev-tool state file.
	if err := supervisor.Run(ctx, child, rv.Journal, rv.Reverters, nil, supervisor.WithRevertLock(lock),
		supervisor.WithLogf(logf), supervisor.WithAfterRevert(rv.Cleanup)); err != nil {
		fmt.Fprintf(stderr, "zatvord: %v\n", err)
		return 1
	}
	return 0
}
