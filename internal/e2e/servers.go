// Package e2e runs Zatvor's whole pipeline (subscription body to running
// Xray) against local Xray servers; the tests carry the xrayint build tag.
// This file starts those servers and the HTTP echo they forward to.
//
// Everything listens on 127.0.0.1 only. Keys, UUIDs, passwords and the TLS
// certificate are generated per run and never written to the repository: the
// server configs in testdata/e2e hold REPLACE_ME_* placeholders, filled by
// walking the parsed JSON (never by string substitution) and handed to Xray on
// stdin through xrayrun.
//
// Proving the intended server carried a request: on loopback every peer
// address is 127.0.0.1, so the echo alone cannot tell which server forwarded
// a request. On Linux, where the whole 127.0.0.0/8 is usable without
// configuration, each server's freedom outbound gets its own sendThrough
// address (Reality 127.0.0.2, XHTTP 127.0.0.3, Hysteria2 127.0.0.4) and the
// test checks the echo saw it. Other systems (Windows, macOS) do not offer
// those addresses everywhere, so sendThrough is left out there. On every
// system the server's access log (log.access in a temp file) must also record
// that it accepted a connection to the echo address.
package e2e

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/ojson"
	"github.com/ArtikGenius/zatvor/internal/xrayrun"
)

// Names the clients use. RealitySNI is in the Reality server's serverNames;
// XHTTPSNI and Hy2SNI are on the self-signed certificate. Hy2SNI is the
// serverName of the Hysteria2-DNS fixtures.
const (
	RealitySNI = "reality.example.com"
	XHTTPSNI   = "xhttp.example.com"
	Hy2SNI     = "hy2.example.com"
	XHTTPPath  = "/e2e-xhttp"
)

// templateDir holds the server config templates, relative to this package
// (the working directory of `go test`).
var templateDir = filepath.Join("..", "..", "testdata", "e2e")

// Keys are one run's server secrets.
type Keys struct {
	UUID          string
	RealityPublic string // the client's pbk
	realityPriv   string
	ShortID       string
	Hy2Auth       string
	// SalamanderPassword is the Hysteria2 obfuscation password.
	SalamanderPassword string
	// CertSHA256 is the hex SHA-256 of the certificate's DER, for
	// pinnedPeerCertSha256 / pinSHA256.
	CertSHA256 string
	certLines  []any // PEM lines, as Xray's "certificate" list
	keyLines   []any
	cert       tls.Certificate
}

// NewKeys generates fresh secrets: the UUID and the Reality X25519 pair with
// the pinned Xray binary, the rest with crypto/rand, and a self-signed ECDSA
// certificate for RealitySNI, XHTTPSNI and Hy2SNI.
func NewKeys(t testing.TB, xray string) Keys {
	t.Helper()
	var k Keys
	k.UUID = strings.TrimSpace(xrayOutput(t, xray, "uuid"))
	for _, line := range strings.Split(xrayOutput(t, xray, "x25519"), "\n") {
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch val = strings.TrimSpace(val); {
		case strings.Contains(name, "PrivateKey"):
			k.realityPriv = val
		case strings.Contains(name, "PublicKey"):
			k.RealityPublic = val
		}
	}
	if k.UUID == "" || k.realityPriv == "" || k.RealityPublic == "" {
		t.Fatal("xray uuid / x25519: unexpected output format")
	}
	var err error
	for _, p := range []struct {
		dst *string
		n   int
	}{{&k.ShortID, 8}, {&k.Hy2Auth, 16}, {&k.SalamanderPassword, 16}} {
		if *p.dst, err = RandomHex(p.n); err != nil {
			t.Fatal(err)
		}
	}
	if err := k.makeCert(); err != nil {
		t.Fatal(err)
	}
	return k
}

func xrayOutput(t testing.TB, xray string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, xray, args...).Output()
	if err != nil {
		t.Fatalf("xray %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

func (k *Keys) makeCert() error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "zatvor e2e"},
		DNSNames:     []string{RealitySNI, XHTTPSNI, Hy2SNI},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if k.cert, err = tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return err
	}
	sum := sha256.Sum256(der)
	k.CertSHA256 = hex.EncodeToString(sum[:])
	k.certLines, k.keyLines = pemLines(certPEM), pemLines(keyPEM)
	return nil
}

func pemLines(b []byte) []any {
	var out []any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		out = append(out, l)
	}
	return out
}

// RandomHex returns n random bytes as hex.
func RandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Echo is a local HTTP server whose GET /ip answers with the IP address the
// request came from.
type Echo struct {
	Addr string // 127.0.0.1:port
}

func echoHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ip", func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		fmt.Fprintln(w, host)
	})
	return mux
}

// StartEcho starts a plain HTTP echo on 127.0.0.1, closed in t.Cleanup.
func StartEcho(t testing.TB) *Echo {
	t.Helper()
	s := httptest.NewServer(echoHandler())
	t.Cleanup(s.Close)
	return &Echo{Addr: s.Listener.Addr().String()}
}

// StartTLSEcho starts the HTTPS echo used as the Reality server's target
// (TLS 1.3 with k's certificate), on 127.0.0.1 and closed in t.Cleanup.
func StartTLSEcho(t testing.TB, k Keys) *Echo {
	t.Helper()
	s := httptest.NewUnstartedServer(echoHandler())
	s.TLS = &tls.Config{Certificates: []tls.Certificate{k.cert}, MinVersion: tls.VersionTLS13}
	// Reality probes its target with handshakes it abandons; those are
	// expected, not worth a line each in the test output.
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	t.Cleanup(s.Close)
	return &Echo{Addr: s.Listener.Addr().String()}
}

// Server is one running local Xray server.
type Server struct {
	Name string
	Port int // on 127.0.0.1
	// SendThrough is the source address of the server's outbound
	// connections, or "" where the system has none to spare (see the package
	// comment).
	SendThrough string
	accessLog   string
	errorLog    string
}

// StartReality starts a VLESS Reality + xtls-rprx-vision server (raw TCP)
// whose Reality target is target (host:port of a local TLS 1.3 server).
func StartReality(t testing.TB, xray string, k Keys, target string) *Server {
	t.Helper()
	return startServer(t, xray, "reality", "server-reality.json", "tcp", "127.0.0.2", map[string]any{
		"REPLACE_ME_UUID":                k.UUID,
		"REPLACE_ME_TARGET":              target,
		"REPLACE_ME_REALITY_SNI":         RealitySNI,
		"REPLACE_ME_REALITY_PRIVATE_KEY": k.realityPriv,
		"REPLACE_ME_SHORT_ID":            k.ShortID,
	})
}

// StartXHTTP starts a VLESS XHTTP (mode packet-up, path XHTTPPath) server
// over TLS with k's certificate.
func StartXHTTP(t testing.TB, xray string, k Keys) *Server {
	t.Helper()
	return startServer(t, xray, "xhttp", "server-xhttp.json", "tcp", "127.0.0.3", map[string]any{
		"REPLACE_ME_UUID":       k.UUID,
		"REPLACE_ME_CERT":       k.certLines,
		"REPLACE_ME_KEY":        k.keyLines,
		"REPLACE_ME_XHTTP_PATH": XHTTPPath,
	})
}

// StartHysteria2 starts a Hysteria2 server (UDP) with k's certificate, the
// salamander password and, in finalmask, the fixture's exp noise item.
func StartHysteria2(t testing.TB, xray string, k Keys) *Server {
	t.Helper()
	return startServer(t, xray, "hysteria2", "server-hysteria2.json", "udp", "127.0.0.4", map[string]any{
		"REPLACE_ME_HY2_AUTH":            k.Hy2Auth,
		"REPLACE_ME_CERT":                k.certLines,
		"REPLACE_ME_KEY":                 k.keyLines,
		"REPLACE_ME_SALAMANDER_PASSWORD": k.SalamanderPassword,
	})
}

func startServer(t testing.TB, xray, name, template, network, sendThrough string, vals map[string]any) *Server {
	t.Helper()
	dir := t.TempDir()
	s := &Server{
		Name:      name,
		accessLog: filepath.Join(dir, "access.log"),
		errorLog:  filepath.Join(dir, "error.log"),
	}
	port, err := freePort(network)
	if err != nil {
		t.Fatal(err)
	}
	s.Port = port
	vals["REPLACE_ME_PORT"] = json.Number(strconv.Itoa(port))
	vals["REPLACE_ME_ACCESS_LOG"] = s.accessLog
	vals["REPLACE_ME_ERROR_LOG"] = s.errorLog
	if runtime.GOOS == "linux" {
		s.SendThrough = sendThrough
		vals["REPLACE_ME_SEND_THROUGH"] = sendThrough
	} else {
		vals["REPLACE_ME_SEND_THROUGH"] = nil // drop the key
	}

	tmpl, err := os.ReadFile(filepath.Join(templateDir, template))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Fill(tmpl, vals)
	if err != nil {
		t.Fatalf("%s: %v", template, err)
	}

	r := xrayrun.New(xray, dir, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.Start(ctx, cfg, logReady(s.errorLog)); err != nil {
		t.Fatalf("start %s server: %v\n%s", name, err, s.Logs())
	}
	pid := r.Pid()
	t.Cleanup(func() {
		if err := r.Stop(5 * time.Second); err != nil {
			t.Errorf("stop %s server (pid %d): %v", name, pid, err)
		}
	})
	return s
}

// logReady waits until Xray's error log reports it started, which it does
// after every inbound is listening (TCP and UDP alike).
func logReady(path string) xrayrun.ReadyCheck {
	return func(ctx context.Context) error {
		for {
			if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), " started") {
				return nil
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("no start message in the log: %w", ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
}

func freePort(network string) (int, error) {
	if network == "udp" {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// WaitCarried waits until the access log shows the server accepted a TCP
// connection to dest ("host:port").
func (s *Server) WaitCarried(dest string, timeout time.Duration) error {
	want := "accepted tcp:" + dest + " "
	deadline := time.Now().Add(timeout)
	for {
		if f, err := os.Open(s.accessLog); err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if strings.Contains(sc.Text()+" ", want) {
					f.Close()
					return nil
				}
			}
			f.Close()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("access log has no connection to %s", dest)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Logs returns the server's error and access logs, for failure messages.
func (s *Server) Logs() string {
	var b strings.Builder
	for _, p := range []string{s.errorLog, s.accessLog} {
		c, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(&b, "--- %s: %v\n", filepath.Base(p), err)
			continue
		}
		fmt.Fprintf(&b, "--- %s\n%s", filepath.Base(p), c)
	}
	return b.String()
}

// Fill parses a JSON template and replaces every string value equal to a key
// of vals with that value (any JSON value; nil removes the member from its
// object). A REPLACE_ME_ string left over is an error.
func Fill(template []byte, vals map[string]any) ([]byte, error) {
	v, err := ojson.Parse(template)
	if err != nil {
		return nil, err
	}
	v, err = fill(v, vals)
	if err != nil {
		return nil, err
	}
	return ojson.Marshal(v)
}

var errOmitOutsideObject = errors.New("fill: a removed placeholder must be an object member")

func fill(v any, vals map[string]any) (any, error) {
	switch x := v.(type) {
	case *ojson.Object:
		for _, k := range x.Keys() {
			c, _ := x.Get(k)
			if s, ok := c.(string); ok {
				if r, has := vals[s]; has && r == nil {
					x.Delete(k)
					continue
				}
			}
			nc, err := fill(c, vals)
			if err != nil {
				return nil, err
			}
			x.Set(k, nc)
		}
		return x, nil
	case []any:
		for i, e := range x {
			ne, err := fill(e, vals)
			if err != nil {
				return nil, err
			}
			x[i] = ne
		}
		return x, nil
	case string:
		if r, ok := vals[x]; ok {
			if r == nil {
				return nil, errOmitOutsideObject
			}
			return r, nil
		}
		if strings.HasPrefix(x, "REPLACE_ME_") {
			return nil, fmt.Errorf("fill: no value for placeholder %s", x)
		}
		return x, nil
	default:
		return v, nil
	}
}
