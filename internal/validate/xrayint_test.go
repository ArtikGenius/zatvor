//go:build xrayint

package validate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func xrayPath(t *testing.T) string {
	t.Helper()
	p := os.Getenv("ZATVOR_XRAY")
	if p == "" {
		t.Skip("ZATVOR_XRAY is not set; skipping xray run -test")
	}
	return p
}

func TestXrayTestAccepts(t *testing.T) {
	bin := xrayPath(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg := `{"outbounds":[{"protocol":"freedom","tag":"direct"}]}`
	if err := XrayTest(ctx, bin, []byte(cfg), t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestXrayTestRejects(t *testing.T) {
	bin := xrayPath(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg := `{"outbounds":[{"protocol":"nosuchproto","tag":"p","settings":{"password":"SECRET_REPLACE_ME"}}]}`
	err := XrayTest(ctx, bin, []byte(cfg), t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nosuchproto") {
		t.Errorf("Xray message missing: %v", err)
	}
	if strings.Contains(err.Error(), "SECRET_REPLACE_ME") {
		t.Errorf("secret leaked: %v", err)
	}
}

func TestXrayTestCancel(t *testing.T) {
	bin := xrayPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := XrayTest(ctx, bin, []byte(`{}`), filepath.Join(t.TempDir())); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}
