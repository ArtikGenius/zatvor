package xraybin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func makeZip(t *testing.T, exe string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		exe:           "#!/bin/sh\necho fake xray\n",
		"geoip.dat":   "ignored",
		"geosite.dat": "ignored",
		"LICENSE":     "ignored",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func testPin(zipName, hash string) Pin {
	return Pin{Version: "vTEST", Assets: map[string]Asset{
		"linux-amd64": {Zip: zipName, SHA256: hash},
	}}
}

func serve(zipName string, zipBody []byte, dgstHash string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/"+zipName, func(w http.ResponseWriter, r *http.Request) { w.Write(zipBody) })
	mux.HandleFunc("/"+zipName+".dgst", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("MD5= 00\nSHA1= 11\nSHA2-256= " + dgstHash + "\nSHA2-512= 22\n"))
	})
	return httptest.NewServer(mux)
}

func noXray(t *testing.T, dir string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "xray") || strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("unexpected leftover %q in dest", e.Name())
		}
	}
}

func TestFetchVerifiesPinnedHash(t *testing.T) {
	z := makeZip(t, "xray")
	srv := serve("Xray-linux-64.zip", z, sum(z))
	defer srv.Close()
	dest := t.TempDir()
	p, err := Fetch(context.Background(), srv.Client(), testPin("Xray-linux-64.zip", sum(z)), srv.URL+"/", "linux-amd64", dest)
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(dest, "xray") {
		t.Fatalf("path %q", p)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o100 == 0 {
		t.Errorf("not executable: %v", st.Mode())
	}
	ents, _ := os.ReadDir(dest)
	if len(ents) != 1 {
		t.Errorf("expected only xray in dest, got %d entries", len(ents))
	}
}

func TestFetchExtractsExeOnWindows(t *testing.T) {
	z := makeZip(t, "xray.exe")
	srv := serve("Xray-windows-64.zip", z, sum(z))
	defer srv.Close()
	pin := Pin{Assets: map[string]Asset{"windows-amd64": {Zip: "Xray-windows-64.zip", SHA256: sum(z)}}}
	dest := t.TempDir()
	p, err := Fetch(context.Background(), srv.Client(), pin, srv.URL, "windows-amd64", dest)
	if err != nil || filepath.Base(p) != "xray.exe" {
		t.Fatalf("p=%q err=%v", p, err)
	}
}

func TestFetchRejectsHashMismatch(t *testing.T) {
	z := makeZip(t, "xray")
	bad := append([]byte(nil), z...)
	bad[len(bad)/2] ^= 0xff
	srv := serve("Xray-linux-64.zip", bad, sum(z))
	defer srv.Close()
	dest := t.TempDir()
	_, err := Fetch(context.Background(), srv.Client(), testPin("Xray-linux-64.zip", sum(z)), srv.URL, "linux-amd64", dest)
	if err == nil || !strings.Contains(err.Error(), "Xray-linux-64.zip") {
		t.Fatalf("err = %v", err)
	}
	noXray(t, dest)
}

func TestFetchRejectsDgstDisagreement(t *testing.T) {
	z := makeZip(t, "xray")
	srv := serve("Xray-linux-64.zip", z, strings.Repeat("ab", 32))
	defer srv.Close()
	dest := t.TempDir()
	_, err := Fetch(context.Background(), srv.Client(), testPin("Xray-linux-64.zip", sum(z)), srv.URL, "linux-amd64", dest)
	if err == nil || !strings.Contains(err.Error(), "dgst") {
		t.Fatalf("err = %v", err)
	}
	noXray(t, dest)
}

func TestFetchLeavesNoPartialFile(t *testing.T) {
	z := makeZip(t, "xray")
	mux := http.NewServeMux()
	mux.HandleFunc("/Xray-linux-64.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		w.Write(z[:10])
		w.(http.Flusher).Flush()
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		c.Close()
	})
	mux.HandleFunc("/Xray-linux-64.zip.dgst", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("SHA2-256= " + sum(z) + "\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dest := t.TempDir()
	_, err := Fetch(context.Background(), srv.Client(), testPin("Xray-linux-64.zip", sum(z)), srv.URL, "linux-amd64", dest)
	if err == nil {
		t.Fatal("expected error")
	}
	noXray(t, dest)
}

func TestFetchUnknownPlatform(t *testing.T) {
	_, err := Fetch(context.Background(), http.DefaultClient, testPin("a.zip", "00"), "http://x/", "plan9-386", t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFetchRejectsZipSlipAndNestedEntries(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range []string{"../xray", "sub/xray"} {
		w, _ := zw.Create(n)
		w.Write([]byte("evil"))
	}
	zw.Close()
	z := buf.Bytes()
	srv := serve("Xray-linux-64.zip", z, sum(z))
	defer srv.Close()
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	_, err := Fetch(context.Background(), srv.Client(), testPin("Xray-linux-64.zip", sum(z)), srv.URL, "linux-amd64", dest)
	if err == nil || !strings.Contains(err.Error(), "not found in archive") {
		t.Fatalf("err = %v", err)
	}
	noXray(t, dest)
	if _, err := os.Stat(filepath.Join(parent, "xray")); err == nil {
		t.Fatal("file written outside destDir")
	}
}

func TestFetchFailureLeavesNoDestDir(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "never")
	_, err := Fetch(context.Background(), http.DefaultClient, testPin("a.zip", "00"), "http://x/", "plan9-386", dest)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("destDir created despite invalid platform")
	}
}

func TestRepoPinPath(t *testing.T) {
	p, err := RepoPinPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPin(p); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedVersionMatchesPinFile(t *testing.T) {
	p, err := RepoPinPath()
	if err != nil {
		t.Fatal(err)
	}
	pin, err := LoadPin(p)
	if err != nil {
		t.Fatal(err)
	}
	if PinnedVersion != pin.Version {
		t.Errorf("PinnedVersion = %q, pin.json has %q", PinnedVersion, pin.Version)
	}
}
