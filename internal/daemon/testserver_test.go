//go:build systemtest && windows

// This file is part of the live acceptance tests only (a test file with
// build tags systemtest and windows, so no build of a command ever links
// internal/e2e; R55): it starts the local servers the live daemon connects
// to, reusing the internal/e2e helpers.

package daemon

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/ArtikGenius/zatvor/internal/e2e"
	"github.com/ArtikGenius/zatvor/internal/link"
)

// Remarks of the two profiles the subscription serves.
const (
	liveRealityName = "live reality vision"
	liveHy2Name     = "live hysteria2"
)

// liveServers are the local servers of one live test, all on 127.0.0.1 and
// stopped in t.Cleanup: a VLESS Reality + Vision server (whose Reality
// target is a local TLS echo), a Hysteria2 server, the HTTP echo the test
// requests through the tunnel, two IP echoes for the tunnel check (R6: no
// third-party service), and the subscription server serving share links
// to both proxy servers.
type liveServers struct {
	Reality, Hy2 *e2e.Server
	Echo         *e2e.Echo // the target of the test's own request
	// Checks are the tunnel check's services, for Options.CheckServices:
	// both answer the caller's address, so they agree on the exit address.
	Checks []string
	// SubURL is the subscription URL (http://127.0.0.1:<port>/<random>).
	// It is a secret like any subscription URL and is never logged.
	SubURL string
	// SubHits counts the requests to SubURL.
	SubHits *atomic.Int32
}

func startLiveServers(t *testing.T, xray string) *liveServers {
	t.Helper()
	keys := e2e.NewKeys(t, xray)
	target := e2e.StartTLSEcho(t, keys)
	s := &liveServers{
		Reality: e2e.StartReality(t, xray, keys, target.Addr),
		Hy2:     e2e.StartHysteria2(t, xray, keys),
		Echo:    e2e.StartEcho(t),
		SubHits: new(atomic.Int32),
	}
	for range 2 {
		s.Checks = append(s.Checks, "http://"+e2e.StartEcho(t).Addr+"/ip")
	}

	reality := link.Export(link.Node{
		Scheme:  "vless",
		Remark:  liveRealityName,
		Address: "127.0.0.1",
		Port:    s.Reality.Port,
		Params: map[string]string{
			"id":         keys.UUID,
			"encryption": "none",
			"flow":       "xtls-rprx-vision",
			"security":   "reality",
			"sni":        e2e.RealitySNI,
			"fp":         "chrome",
			"pbk":        keys.RealityPublic,
			"sid":        keys.ShortID,
			"type":       "tcp",
		},
	})
	q := url.Values{}
	q.Set("sni", e2e.Hy2SNI)
	q.Set("pinSHA256", keys.CertSHA256)
	q.Set("obfs", "salamander")
	q.Set("obfs-password", keys.SalamanderPassword)
	hy2 := "hysteria2://" + url.PathEscape(keys.Hy2Auth) + "@127.0.0.1:" + strconv.Itoa(s.Hy2.Port) +
		"/?" + q.Encode() + "#" + url.PathEscape(liveHy2Name)
	body := []byte(reality + "\n" + hy2 + "\n")

	path, err := e2e.RandomHex(12)
	if err != nil {
		t.Fatal(err)
	}
	path = "/sub/" + path
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) {
		s.SubHits.Add(1)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(body)
	})
	sub := httptest.NewServer(mux)
	t.Cleanup(sub.Close)
	s.SubURL = sub.URL + path
	return s
}
