package validate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxMessage = 4096

// plainKeys are JSON keys whose string values are known to be non-secret and
// stay readable in error messages. Every other string leaf of the config
// (at least minSecretLen long) is treated as a secret.
var plainKeys = setOf("protocol", "tag", "network", "security", "type", "flow", "fingerprint", "alpn",
	"loglevel", "domainstrategy", "outboundtag", "inboundtag", "balancertag", "listen", "mode")

const minSecretLen = 6

// ErrXrayNotRun marks an Xray binary that could not be started.
var ErrXrayNotRun = errors.New("validate: xray could not be started")

// XrayTestError is Xray rejecting a config: its exit code and its output with
// every secret value of the config replaced by ***.
type XrayTestError struct {
	ExitCode int
	Output   string
}

func (e *XrayTestError) Error() string {
	return fmt.Sprintf("xray run -test failed (exit status %d): %s", e.ExitCode, e.Output)
}

// XrayTest runs `xray run -test -config stdin:` on cfg with
// XRAY_LOCATION_ASSET=assetDir in an otherwise minimal environment. The
// context cancels the process (the error then wraps ctx.Err()). A rejected
// config is a *XrayTestError; a binary that cannot be started is
// ErrXrayNotRun.
func XrayTest(ctx context.Context, xrayPath string, cfg []byte, assetDir string) error {
	cmd := exec.CommandContext(ctx, xrayPath, "run", "-test", "-config", "stdin:")
	cmd.Env = minimalEnv(assetDir)
	cmd.Stdin = bytes.NewReader(cfg)
	cmd.WaitDelay = 5 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("xray run -test: %w", ctxErr)
	}
	msg := strings.TrimSpace(scrub(out.String(), cfg))
	if len(msg) > maxMessage {
		msg = msg[:maxMessage] + "..."
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &XrayTestError{ExitCode: ee.ExitCode(), Output: msg}
	}
	return fmt.Errorf("%w: %s", ErrXrayNotRun, scrub(err.Error(), cfg))
}

func minimalEnv(assetDir string) []string {
	env := []string{"XRAY_LOCATION_ASSET=" + assetDir}
	if runtime.GOOS == "windows" {
		if r := os.Getenv("SystemRoot"); r != "" {
			env = append(env, "SystemRoot="+r)
		}
	}
	return env
}

// scrub replaces every secret value found in cfg, and its variants Xray may
// print, with ***. A cfg that is not valid JSON yields msg unchanged.
func scrub(msg string, cfg []byte) string {
	return ScrubAll(msg, SecretValues(cfg))
}

// SecretValues returns the secret string values of the JSON document doc:
// every string leaf at least 6 bytes long that is not under a key known to
// be plain (protocol, tag, network, security, ...). It returns nil when doc
// is not valid JSON.
func SecretValues(doc []byte) []string {
	var v any
	if json.Unmarshal(doc, &v) != nil {
		return nil
	}
	leaves := map[string]bool{}
	collectSecrets(v, false, leaves)
	out := make([]string, 0, len(leaves))
	for s := range leaves {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// ScrubAll replaces every variant (SecretVariants) of every secret in msg
// with ***, longest first, so a secret that contains another is replaced
// whole. Secrets shorter than 6 bytes are ignored: they would match
// ordinary words.
func ScrubAll(msg string, secrets []string) string {
	set := map[string]bool{}
	for _, s := range secrets {
		if len(s) < minSecretLen {
			continue
		}
		for _, c := range SecretVariants(s) {
			if c != "" {
				set[c] = true
			}
		}
	}
	if len(set) == 0 {
		return msg
	}
	vals := make([]string, 0, len(set))
	for s := range set {
		vals = append(vals, s)
	}
	sort.Slice(vals, func(i, j int) bool {
		if len(vals[i]) != len(vals[j]) {
			return len(vals[i]) > len(vals[j])
		}
		return vals[i] < vals[j]
	})
	pairs := make([]string, 0, 2*len(vals))
	for _, s := range vals {
		pairs = append(pairs, s, "***")
	}
	// A Replacer tries the pairs in argument order at each position, so
	// the longest match wins.
	return strings.NewReplacer(pairs...).Replace(msg)
}

var uuidRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// SecretVariants lists the forms of a secret that may show up in output:
// as is, lower/upper case, dash-stripped for UUIDs, and of each of these
// the JSON-escaped, Go-quoted, URL query-escaped and URL path-escaped
// forms.
func SecretVariants(s string) []string {
	base := []string{s, strings.ToLower(s), strings.ToUpper(s)}
	if uuidRe.MatchString(s) {
		d := strings.ReplaceAll(s, "-", "")
		base = append(base, d, strings.ToLower(d), strings.ToUpper(d))
	}
	out := append([]string(nil), base...)
	for _, b := range base {
		if j, err := json.Marshal(b); err == nil {
			out = append(out, string(j[1:len(j)-1]))
		}
		q := strconv.Quote(b)
		out = append(out, q[1:len(q)-1], url.QueryEscape(b), url.PathEscape(b))
	}
	return out
}

func collectSecrets(v any, plain bool, set map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			collectSecrets(e, plainKeys[strings.ToLower(k)], set)
		}
	case []any:
		for _, e := range x {
			collectSecrets(e, plain, set)
		}
	case string:
		if !plain && len(x) >= minSecretLen {
			set[x] = true
		}
	}
}

// MinimalEnv is the environment for every Xray child process: only
// XRAY_LOCATION_ASSET (plus SystemRoot on Windows).
func MinimalEnv(assetDir string) []string { return minimalEnv(assetDir) }

// Scrub replaces every secret value of cfg found in msg with ***.
func Scrub(msg string, cfg []byte) string { return scrub(msg, cfg) }
