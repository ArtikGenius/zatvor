package validate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

const maxMessage = 4096

// secretKeys are JSON keys whose string values are secrets (or user names) and
// must never appear in an error message.
var secretKeys = setOf("id", "password", "pass", "privatekey", "publickey", "shortid", "shortids",
	"presharedkey", "secretkey", "auth", "user", "username", "email")

// XrayTest runs `xray run -test -config stdin:` on cfg with
// XRAY_LOCATION_ASSET=assetDir in an otherwise minimal environment. The
// context cancels the process. The error carries Xray's output with every
// secret value from cfg replaced by ***.
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
		return fmt.Errorf("xray run -test failed (%v): %s", ee, msg)
	}
	return fmt.Errorf("xray run -test: %v: %s", err, msg)
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

// scrub replaces every secret value found in cfg with ***. A cfg that is not
// valid JSON yields msg unchanged.
func scrub(msg string, cfg []byte) string {
	var v any
	if json.Unmarshal(cfg, &v) != nil {
		return msg
	}
	set := map[string]bool{}
	collectSecrets(v, false, set)
	vals := make([]string, 0, len(set))
	for s := range set {
		vals = append(vals, s)
	}
	// Longest first so a secret containing another is replaced whole.
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, s := range vals {
		msg = strings.ReplaceAll(msg, s, "***")
	}
	return msg
}

func collectSecrets(v any, secret bool, set map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			collectSecrets(e, secretKeys[strings.ToLower(k)], set)
		}
	case []any:
		for _, e := range x {
			collectSecrets(e, secret, set)
		}
	case string:
		if secret && x != "" {
			set[x] = true
		}
	}
}
