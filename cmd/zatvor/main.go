// Command zatvor is the user's command line for the Zatvor daemon: it shows
// the connection, runs a tunnel check and saves a diagnostics bundle. It
// talks to zatvord's local API only (ADR 0009): it finds the port in the
// data directory's run/api.json and the token in run/token, and sends
// requests to 127.0.0.1 with that token, never through a proxy and never
// following a redirect. It never prints the token or a subscription URL.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ArtikGenius/zatvor/internal/api"
	"github.com/ArtikGenius/zatvor/internal/appdir"
	"github.com/ArtikGenius/zatvor/internal/engine"
)

// Exit codes. check uses all of them; the other commands use 0, 1 (an
// error), 2 (a usage error) and 3.
const (
	exitOK         = 0
	exitBad        = 1 // check: not connected, or the check is bad
	exitUnknown    = 2 // check: the check could not decide or run; any command: a usage error
	exitNotRunning = 3 // the daemon is not running
	exitError      = 1 // status, diag: the command failed
)

const (
	shortTimeout = 5 * time.Second  // status
	longTimeout  = 60 * time.Second // check and diag
)

// maxResponse is the largest answer accepted (a diagnostics zip); a larger
// one is an error, never a cut-off file. A variable for tests.
var maxResponse int64 = 64 << 20

const usage = `Usage: zatvor <command> [flags]

Commands:
  status [--json]              show the phase, profile, ports, last tunnel check and warnings
  check [--json]               run a fresh tunnel check through the connected tunnel
  diag -o FILE.zip [--force]   save the diagnostics bundle (secrets redacted); --force
                               replaces an existing FILE.zip

Every command takes --data-dir DIR: the daemon's data directory (default:
the per-user data directory of this OS, as for zatvord).

Exit codes of check:
  0  connected and the tunnel check is OK
  1  not connected, or the check is bad (traffic does not go through Zatvor)
  2  unknown: the check could not decide or could not run; also a usage error
  3  the daemon is not running

Exit codes of status and diag:
  0  done
  1  the command failed
  2  a usage error
  3  the daemon is not running
`

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }

func run(args []string, env func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUnknown
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "status":
		return cmdStatus(args[1:], env, stdout, stderr)
	case "check":
		return cmdCheck(args[1:], env, stdout, stderr)
	case "diag":
		return cmdDiag(args[1:], env, stdout, stderr)
	}
	fmt.Fprintf(stderr, "zatvor: unknown command %q\n\n%s", args[0], usage)
	return exitUnknown
}

// errUsage: the flags were wrong; the message is already printed.
var errUsage = errors.New("usage")

// errNotRunning: no daemon answers for this data directory.
var errNotRunning = errors.New("the daemon is not running")

// commonFlags parses the flags of one command. --data-dir is shared; add
// registers the command's own flags.
func commonFlags(name string, args []string, env func(string) string, stderr io.Writer, add func(*flag.FlagSet)) (appdir.Dirs, error) {
	fs := flag.NewFlagSet("zatvor "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "the daemon's data directory (default: the per-user directory of this OS)")
	add(fs)
	if err := fs.Parse(args); err != nil {
		return appdir.Dirs{}, errUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "zatvor %s: unexpected argument %q\n", name, fs.Arg(0))
		return appdir.Dirs{}, errUsage
	}
	base := *dataDir
	if base != "" {
		if err := appdir.ValidateDataDir(base, appdir.Home(env)); err != nil {
			fmt.Fprintf(stderr, "zatvor %s: --data-dir: %v\n", name, err)
			return appdir.Dirs{}, errUsage
		}
	} else {
		var err error
		if base, err = appdir.Default(env); err != nil {
			return appdir.Dirs{}, err
		}
	}
	// Layout only: a client never creates or changes the data directory.
	return appdir.Layout(base), nil
}

// ---- the API client ----

type client struct {
	base  string // http://127.0.0.1:<port>
	token string
	http  *http.Client
}

// apiError is an error response of the API.
type apiError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return e.Message + " (" + e.Code + ")" }

// connect finds the daemon of dirs. A missing or damaged discovery or
// token file, or a discovery file that changes while it is read (a daemon
// starting or stopping), means it is not running.
func connect(dirs appdir.Dirs, timeout time.Duration) (*client, error) {
	d, err := api.ReadDiscovery(dirs)
	if err != nil {
		return nil, errNotRunning
	}
	tok, err := api.ReadToken(dirs)
	if err != nil {
		return nil, errNotRunning
	}
	if again, err := api.ReadDiscovery(dirs); err != nil || again != d {
		return nil, errNotRunning
	}
	return &client{
		base:  fmt.Sprintf("http://127.0.0.1:%d", d.Port),
		token: tok,
		http: &http.Client{
			Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       timeout,
		},
	}, nil
}

// do sends one request and returns the body of a 200 answer with its
// Content-Type. A port that refuses the connection, or answers 403 (our
// token is not the one of whoever listens there now), means the daemon is
// not running; another non-200 answer is an *apiError.
func (c *client) do(method, path string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, nil)
	if err != nil {
		return nil, "", err
	}
	// The URL sets Host to exactly 127.0.0.1:<port>, as the API requires.
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		var oe *net.OpError
		if errors.As(err, &oe) && oe.Op == "dial" {
			return nil, "", errNotRunning
		}
		return nil, "", fmt.Errorf("the daemon did not answer: %w", errors.Unwrap(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading the daemon's answer: %w", err)
	}
	if int64(len(body)) > maxResponse {
		return nil, "", fmt.Errorf("the daemon's answer is larger than %d bytes", maxResponse)
	}
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode == http.StatusOK {
		return body, ct, nil
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, "", errNotRunning
	}
	ae := &apiError{Status: resp.StatusCode}
	if !strings.HasPrefix(ct, "application/json") || json.Unmarshal(body, ae) != nil || ae.Code == "" {
		ae.Code, ae.Message = fmt.Sprintf("http_%d", resp.StatusCode), "unexpected answer from the daemon"
	}
	return nil, "", ae
}

// getJSON decodes the JSON answer of method path into v.
func (c *client) getJSON(method, path string, v any) error {
	body, ct, err := c.do(method, path)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(ct, "application/json") || json.Unmarshal(body, v) != nil {
		return errors.New("unexpected answer from the daemon")
	}
	return nil
}

// ---- status ----

// statusJSON is the stable output of `status --json`; it does not follow
// the API's field names.
type statusJSON struct {
	Phase        string       `json:"phase"`
	Profile      *profileJSON `json:"profile"`
	Ports        portsJSON    `json:"ports"`
	Check        checkJSON    `json:"check"`
	Warnings     []issueJSON  `json:"warnings"`
	Error        *issueJSON   `json:"error"`
	Safe         bool         `json:"safe"`
	GeoBootstrap bool         `json:"geoBootstrap"`
}

type profileJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type portsJSON struct {
	Mixed    int `json:"mixed"`
	Probe    int `json:"probe"`
	DevTools int `json:"devTools"`
	API      int `json:"api"`
}

type checkJSON struct {
	Status string     `json:"status"`
	IPv4   string     `json:"ipv4"`
	IPv6   string     `json:"ipv6"`
	At     *time.Time `json:"at"` // null: never checked
}

type issueJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func checkOf(c engine.CheckResult) checkJSON {
	out := checkJSON{Status: string(c.Status), IPv4: c.IPv4, IPv6: c.IPv6}
	if !c.At.IsZero() {
		at := c.At.UTC()
		out.At = &at
	}
	return out
}

func statusOf(st engine.State, profiles []api.ProfileInfo) statusJSON {
	out := statusJSON{
		Phase:        string(st.Phase),
		Ports:        portsJSON{st.Ports.Mixed, st.Ports.Probe, st.Ports.DevTools, st.Ports.API},
		Check:        checkOf(st.Check),
		Warnings:     []issueJSON{},
		Safe:         st.Safe,
		GeoBootstrap: st.GeoBootstrap,
	}
	if st.Profile != "" {
		out.Profile = &profileJSON{ID: st.Profile}
		for _, p := range profiles {
			if p.ID == st.Profile {
				out.Profile.Name = p.Name
			}
		}
	}
	for _, w := range st.Warnings {
		out.Warnings = append(out.Warnings, issueJSON{w.Code, w.Message})
	}
	if st.Err != nil {
		out.Error = &issueJSON{st.Err.Code, st.Err.Message}
	}
	return out
}

func cmdStatus(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var asJSON *bool
	dirs, err := commonFlags("status", args, env, stderr, func(fs *flag.FlagSet) {
		asJSON = fs.Bool("json", false, "print stable JSON")
	})
	if err != nil {
		return failCode(err, stderr, exitError)
	}
	c, err := connect(dirs, shortTimeout)
	if err != nil {
		return failCode(err, stderr, exitError)
	}
	var st engine.State
	if err := c.getJSON("GET", "/v1/status", &st); err != nil {
		return failCode(err, stderr, exitError)
	}
	var pl struct {
		Profiles []api.ProfileInfo `json:"profiles"`
	}
	if st.Profile != "" {
		_ = c.getJSON("GET", "/v1/profiles", &pl) // the name only; the ID is enough without it
	}
	out := statusOf(st, pl.Profiles)
	if *asJSON {
		printJSON(stdout, out)
		return exitOK
	}
	printStatus(stdout, out)
	return exitOK
}

func printStatus(w io.Writer, s statusJSON) {
	mode := "live mode"
	if s.Safe {
		mode = "safe mode: no system changes"
	}
	fmt.Fprintf(w, "phase:    %s (%s)\n", s.Phase, mode)
	if s.Profile != nil {
		if s.Profile.Name != "" {
			fmt.Fprintf(w, "profile:  %s (%s)\n", s.Profile.Name, s.Profile.ID)
		} else {
			fmt.Fprintf(w, "profile:  %s\n", s.Profile.ID)
		}
	}
	if s.Ports.Mixed != 0 {
		fmt.Fprintf(w, "ports:    proxy 127.0.0.1:%d, probe %d, dev tools %d, xray api %d\n",
			s.Ports.Mixed, s.Ports.Probe, s.Ports.DevTools, s.Ports.API)
	}
	if s.Check.At != nil {
		fmt.Fprintf(w, "check:    %s%s at %s\n", s.Check.Status, exitAddr(s.Check), s.Check.At.Local().Format(time.DateTime))
	} else {
		fmt.Fprintln(w, "check:    not run yet")
	}
	if s.GeoBootstrap {
		fmt.Fprintln(w, "geo:      downloading the profile's geo files; everything goes through the tunnel meanwhile")
	}
	if s.Error != nil {
		fmt.Fprintf(w, "error:    %s: %s\n", s.Error.Code, s.Error.Message)
	}
	for _, x := range s.Warnings {
		fmt.Fprintf(w, "warning:  %s: %s\n", x.Code, x.Message)
	}
}

func exitAddr(c checkJSON) string {
	var a []string
	for _, ip := range []string{c.IPv4, c.IPv6} {
		if ip != "" {
			a = append(a, ip)
		}
	}
	if len(a) == 0 {
		return ""
	}
	return ", exit address " + strings.Join(a, ", ")
}

// ---- check ----

// checkOut is the output of `check --json`. Result is ok, bad, unknown,
// not_connected, error or daemon_not_running; Exit is the exit code.
type checkOut struct {
	Result string     `json:"result"`
	Exit   int        `json:"exit"`
	Check  *checkJSON `json:"check,omitempty"`
	Error  *issueJSON `json:"error,omitempty"`
}

func cmdCheck(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var asJSON *bool
	dirs, err := commonFlags("check", args, env, stderr, func(fs *flag.FlagSet) {
		asJSON = fs.Bool("json", false, "print the result as JSON")
	})
	if errors.Is(err, errUsage) {
		return exitUnknown
	}
	out := runCheck(dirs, err)
	if *asJSON {
		printJSON(stdout, out)
		return out.Exit
	}
	switch out.Result {
	case "ok":
		fmt.Fprintf(stdout, "connected; the tunnel check is OK%s\n", exitAddr(*out.Check))
	case "bad":
		fmt.Fprintln(stdout, "the tunnel check is BAD: the IP-check services disagree; traffic may not go through Zatvor")
	case "unknown":
		fmt.Fprintln(stdout, "the tunnel check could not decide: too few IP-check services answered through the tunnel")
	case "not_connected":
		fmt.Fprintln(stdout, "not connected")
	case "daemon_not_running":
		fmt.Fprintln(stderr, "zatvor: the daemon is not running")
	default:
		fmt.Fprintf(stderr, "zatvor: the tunnel check did not run: %s\n", out.Error.Message)
	}
	return out.Exit
}

// runCheck asks the daemon for a fresh check; setupErr is an error from
// before (finding the data directory).
func runCheck(dirs appdir.Dirs, setupErr error) checkOut {
	failed := func(err error) checkOut {
		if errors.Is(err, errNotRunning) {
			return checkOut{Result: "daemon_not_running", Exit: exitNotRunning,
				Error: &issueJSON{"daemon_not_running", errNotRunning.Error()}}
		}
		var ae *apiError
		if errors.As(err, &ae) {
			if ae.Code == engine.CodeNotConnected {
				return checkOut{Result: "not_connected", Exit: exitBad, Error: &issueJSON{ae.Code, ae.Message}}
			}
			return checkOut{Result: "error", Exit: exitUnknown, Error: &issueJSON{ae.Code, ae.Message}}
		}
		return checkOut{Result: "error", Exit: exitUnknown, Error: &issueJSON{"error", err.Error()}}
	}
	if setupErr != nil {
		return failed(setupErr)
	}
	c, err := connect(dirs, longTimeout)
	if err != nil {
		return failed(err)
	}
	var res engine.CheckResult
	if err := c.getJSON("POST", "/v1/check", &res); err != nil {
		return failed(err)
	}
	cj := checkOf(res)
	switch res.Status {
	case engine.OK:
		return checkOut{Result: "ok", Exit: exitOK, Check: &cj}
	case engine.Bad:
		return checkOut{Result: "bad", Exit: exitBad, Check: &cj}
	default: // Unknown, and anything this build does not know
		return checkOut{Result: "unknown", Exit: exitUnknown, Check: &cj}
	}
}

// ---- diag ----

func cmdDiag(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var out *string
	var force *bool
	dirs, err := commonFlags("diag", args, env, stderr, func(fs *flag.FlagSet) {
		out = fs.String("o", "", "the zip file to write (required)")
		force = fs.Bool("force", false, "replace the file if it exists")
	})
	if err != nil {
		return failCode(err, stderr, exitError)
	}
	if *out == "" {
		fmt.Fprintln(stderr, "zatvor diag: -o FILE.zip is required")
		return exitUnknown
	}
	path := *out
	if !*force {
		// Early, to skip the download; writeFile checks again atomically.
		if _, err := os.Lstat(path); err == nil {
			fmt.Fprintf(stderr, "zatvor diag: %v\n", existsError(path))
			return exitError
		}
	}
	c, err := connect(dirs, longTimeout)
	if err != nil {
		return failCode(err, stderr, exitError)
	}
	body, ct, err := c.do("GET", "/v1/diag")
	if err != nil {
		return failCode(err, stderr, exitError)
	}
	if ct != "application/zip" {
		fmt.Fprintln(stderr, "zatvor diag: unexpected answer from the daemon")
		return exitError
	}
	if err := writeFile(path, body, *force); err != nil {
		fmt.Fprintf(stderr, "zatvor diag: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "saved the diagnostics to %s\n", path)
	return exitOK
}

// writeFile writes data to path atomically: a temp file in the same
// directory, fsynced, then put in place. With force it is renamed over
// path. Without, it is hard-linked to path, which fails when path exists
// however late that file appeared, so nothing is ever replaced; on a file
// system without hard links path is created exclusively instead.
func writeFile(path string, data []byte, force bool) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".zatvor-diag-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if force {
		return os.Rename(tmp, path)
	}
	switch lerr := os.Link(tmp, path); {
	case lerr == nil:
		os.Remove(tmp)
		return nil
	case errors.Is(lerr, fs.ErrExist):
		return existsError(path)
	}
	os.Remove(tmp)
	return writeExclusive(path, data)
}

// writeExclusive creates path, failing when it exists, and writes data.
// A failed write removes the file it created.
func writeExclusive(path string, data []byte) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return existsError(path)
	}
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(path)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

func existsError(path string) error {
	return fmt.Errorf("%s exists; use --force to replace it", path)
}

// ---- output ----

func printJSON(w io.Writer, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	w.Write(append(b, '\n'))
}

// failCode prints err and returns its exit code: 2 for a usage error
// (already printed), 3 when the daemon is not running, else code.
func failCode(err error, stderr io.Writer, code int) int {
	switch {
	case errors.Is(err, errUsage):
		return exitUnknown
	case errors.Is(err, errNotRunning):
		fmt.Fprintln(stderr, "zatvor: the daemon is not running")
		return exitNotRunning
	}
	fmt.Fprintf(stderr, "zatvor: %v\n", err)
	return code
}
