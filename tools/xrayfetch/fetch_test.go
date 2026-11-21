package main

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
	p, err := FetchWithPin(context.Background(), srv.Client(), srv.URL+"/", testPin("Xray-linux-64.zip", sum(z)), "linux-amd64", dest)
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
	p, err := FetchWithPin(context.Background(), srv.Client(), srv.URL, pin, "windows-amd64", dest)
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
	_, err := FetchWithPin(context.Background(), srv.Client(), srv.URL, testPin("Xray-linux-64.zip", sum(z)), "linux-amd64", dest)
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
	_, err := FetchWithPin(context.Background(), srv.Client(), srv.URL, testPin("Xray-linux-64.zip", sum(z)), "linux-amd64", dest)
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
	_, err := FetchWithPin(context.Background(), srv.Client(), srv.URL, testPin("Xray-linux-64.zip", sum(z)), "linux-amd64", dest)
	if err == nil {
		t.Fatal("expected error")
	}
	noXray(t, dest)
}

func TestFetchUnknownPlatform(t *testing.T) {
	_, err := FetchWithPin(context.Background(), http.DefaultClient, "http://x/", testPin("a.zip", "00"), "plan9-386", t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
}
