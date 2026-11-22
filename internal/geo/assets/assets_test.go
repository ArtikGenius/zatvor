package assets

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/geo/merge"
	"github.com/ArtikGenius/zatvor/internal/geo/synth"
)

func cfgFor(site, ip string) []byte {
	return []byte(`{"routing":{"rules":[{"domain":["geosite:` + site + `"],"ip":["geoip:` + ip + `"]}]}}`)
}

// synthPair writes a synthetic geosite/geoip pair and returns their paths.
func synthPair(t *testing.T, site, ip []string) (sitePath, ipPath string) {
	t.Helper()
	s, i, err := synth.Write(t.TempDir(), site, ip)
	if err != nil {
		t.Fatal(err)
	}
	return s, i
}

func copyTo(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

type env struct {
	a                      *Assets
	bundled, cache, merged string
}

// newEnv builds a bundled dir: roscomvpn has site "rsite"+"shared" and ip
// "rip"; v2fly has site "vsite" and ip "vip".
func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{bundled: t.TempDir(), cache: filepath.Join(t.TempDir(), "cache"), merged: filepath.Join(t.TempDir(), "merged")}
	rs, ri := synthPair(t, []string{"rsite", "shared"}, []string{"rip"})
	vs, vi := synthPair(t, []string{"vsite"}, []string{"vip"})
	copyTo(t, rs, filepath.Join(e.bundled, RoscomGeoSite))
	copyTo(t, ri, filepath.Join(e.bundled, RoscomGeoIP))
	copyTo(t, vs, filepath.Join(e.bundled, V2flyGeoSite))
	copyTo(t, vi, filepath.Join(e.bundled, V2flyGeoIP))
	e.a = New(e.bundled, e.cache, e.merged)
	return e
}

func (e *env) writeSources(t *testing.T, geoipURL, geositeURL string) {
	t.Helper()
	m := map[string]PinFile{
		RoscomGeoIP:   {URL: geoipURL},
		RoscomGeoSite: {URL: geositeURL},
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(e.bundled, SourcesFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func completeDirs(t *testing.T, root string) []string {
	t.Helper()
	ents, _ := os.ReadDir(root)
	var out []string
	for _, d := range ents {
		if _, err := os.Stat(filepath.Join(root, d.Name(), markerName)); err == nil {
			out = append(out, d.Name())
		}
	}
	return out
}

func TestPrepareReusesCompleteDir(t *testing.T) {
	e := newEnv(t)
	cfg := cfgFor("rsite", "vip")
	d1, missing, err := e.a.Prepare(cfg, nil)
	if err != nil || len(missing) != 0 {
		t.Fatalf("prepare: %v %v", err, missing)
	}
	st1, _ := os.Stat(filepath.Join(d1, "geosite.dat"))
	d2, _, err := e.a.Prepare(cfg, nil)
	if err != nil || d1 != d2 {
		t.Fatalf("second: %v %q %q", err, d1, d2)
	}
	st2, _ := os.Stat(filepath.Join(d2, "geosite.dat"))
	if !os.SameFile(st1, st2) {
		t.Fatal("dir was rebuilt")
	}
}

func TestPrepareIgnoresDirWithoutMarker(t *testing.T) {
	e := newEnv(t)
	cfg := cfgFor("rsite", "rip")
	d1, _, err := e.a.Prepare(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(d1, markerName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d1, "geosite.dat"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	d2, _, err := e.a.Prepare(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("key changed: %q %q", d1, d2)
	}
	if _, err := os.Stat(filepath.Join(d2, markerName)); err != nil {
		t.Fatal("marker missing after rebuild")
	}
	b, _ := os.ReadFile(filepath.Join(d2, "geosite.dat"))
	if string(b) == "garbage" {
		t.Fatal("incomplete dir was reused")
	}
}

func TestKeepsLastTwo(t *testing.T) {
	e := newEnv(t)
	var dirs []string
	for _, tag := range []string{"rsite", "shared", "vsite"} {
		d, _, err := e.a.Prepare(cfgFor(tag, "rip"), nil)
		if err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, d)
	}
	got := completeDirs(t, e.merged)
	if len(got) != 2 {
		t.Fatalf("complete dirs = %v", got)
	}
	if _, err := os.Stat(dirs[0]); !os.IsNotExist(err) {
		t.Fatal("oldest dir still present")
	}
	for _, d := range dirs[1:] {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("recent dir gone: %v", err)
		}
	}
}

func TestMergeOrderAndMissing(t *testing.T) {
	e := newEnv(t)
	d, missing, err := e.a.Prepare([]byte(`{"routing":{"rules":[{"domain":["geosite:rsite","geosite:vsite","geosite:nope"],"ip":["geoip:nada"]}]}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if d == "" {
		t.Fatal("no dir")
	}
	var names []string
	for _, m := range missing {
		var _ merge.MissingTagError = m
		names = append(names, m.Kind+":"+m.Tag)
	}
	if len(names) != 2 || names[0] != "geosite:nope" || names[1] != "geoip:nada" {
		t.Fatalf("missing = %v", names)
	}
}

// geoServer serves a geosite/geoip pair for URL /geoip.dat and /geosite.dat.
type geoServer struct {
	*httptest.Server
	hits    atomic.Int64
	ipBody  []byte
	siBody  []byte
	ipSum   string // "" = 404, "bad" = wrong hash
	siSum   string
	sumHits atomic.Int64
}

func newGeoServer(t *testing.T, site, ip []string) *geoServer {
	t.Helper()
	s, i := synthPair(t, site, ip)
	g := &geoServer{}
	g.siBody, _ = os.ReadFile(s)
	g.ipBody, _ = os.ReadFile(i)
	g.ipSum, g.siSum = sha(g.ipBody), sha(g.siBody)
	g.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.hits.Add(1)
		switch r.URL.Path {
		case "/geoip.dat":
			w.Write(g.ipBody)
		case "/geosite.dat":
			w.Write(g.siBody)
		case "/geoip.dat.sha256", "/geosite.dat.sha256":
			g.sumHits.Add(1)
			sum := g.ipSum
			if r.URL.Path == "/geosite.dat.sha256" {
				sum = g.siSum
			}
			if sum == "" {
				http.NotFound(w, r)
				return
			}
			if sum == "bad" {
				sum = sha([]byte("other"))
			}
			w.Write([]byte(sum + "  geoip.dat\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

// trust makes a's HTTP client accept the server's test certificate.
func (e *env) trust(g *geoServer) {
	e.a.tlsConfig = g.Client().Transport.(*http.Transport).TLSClientConfig
}

func plainDialer(called *atomic.Int64) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		called.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
}

func TestFetchVerifiesSHA256(t *testing.T) {
	e := newEnv(t)
	good := newGeoServer(t, []string{"psite"}, []string{"pip"})
	e.trust(good)
	var dials atomic.Int64
	pgGood := ProfileGeo{GeoIPURL: good.URL + "/geoip.dat", GeoSiteURL: good.URL + "/geosite.dat"}
	good.ipSum = "" // no checksum published: TLS only
	if err := e.a.Fetch(context.Background(), pgGood, plainDialer(&dials)); err != nil {
		t.Fatal(err)
	}
	if m := e.a.loadMeta(); m.GeoIP == nil || m.GeoIP.ChecksumPublished || m.GeoSite == nil || !m.GeoSite.ChecksumPublished {
		t.Fatalf("meta = %+v %+v", m.GeoIP, m.GeoSite)
	}
	before, _ := os.ReadFile(filepath.Join(e.cache, "geosite.dat"))
	beforeIP, _ := os.ReadFile(filepath.Join(e.cache, "geoip.dat"))
	beforeMeta, _ := os.ReadFile(filepath.Join(e.cache, metaName))

	// Second server: geoip fine, geosite has a wrong published hash. Neither
	// file may be swapped.
	bad := newGeoServer(t, []string{"other"}, []string{"otherip"})
	bad.siSum = "bad"
	e.a.tlsConfig = mergePools(good, bad)
	pgBad := ProfileGeo{GeoIPURL: bad.URL + "/geoip.dat", GeoSiteURL: bad.URL + "/geosite.dat"}
	if err := e.a.Fetch(context.Background(), pgBad, plainDialer(&dials)); err == nil {
		t.Fatal("bad hash accepted")
	}
	after, _ := os.ReadFile(filepath.Join(e.cache, "geosite.dat"))
	afterIP, _ := os.ReadFile(filepath.Join(e.cache, "geoip.dat"))
	afterMeta, _ := os.ReadFile(filepath.Join(e.cache, metaName))
	if string(before) != string(after) || string(beforeIP) != string(afterIP) || string(beforeMeta) != string(afterMeta) {
		t.Fatal("cache touched by a failed fetch")
	}

	// The good profile geo is used by Prepare, and the bundled data is not.
	_, missing, err := e.a.Prepare(cfgFor("psite", "pip"), &pgGood)
	if err != nil || len(missing) != 0 {
		t.Fatalf("prepare with cached profile geo: %v %v", err, missing)
	}
	_, missing, _ = e.a.Prepare(cfgFor("psite", "rip"), nil)
	if len(missing) != 1 {
		t.Fatalf("missing = %v", missing)
	}
}

func mergePools(gs ...*geoServer) *tls.Config {
	pool := x509.NewCertPool()
	for _, g := range gs {
		pool.AddCert(g.Certificate())
	}
	return &tls.Config{RootCAs: pool}
}

func TestFetchSkipsWhenBundledMatches(t *testing.T) {
	e := newEnv(t)
	g := newGeoServer(t, []string{"x"}, []string{"y"})
	e.trust(g)
	e.writeSources(t, g.URL+"/geoip.dat", g.URL+"/geosite.dat")
	var dials atomic.Int64
	pg := ProfileGeo{GeoIPURL: g.URL + "/geoip.dat", GeoSiteURL: g.URL + "/geosite.dat"}
	if err := e.a.Fetch(context.Background(), pg, plainDialer(&dials)); err != nil {
		t.Fatal(err)
	}
	if g.hits.Load() != 0 || dials.Load() != 0 {
		t.Fatalf("requests=%d dials=%d, want none", g.hits.Load(), dials.Load())
	}
}

func TestFetchSkipsWhenPublishedHashMatchesBundled(t *testing.T) {
	e := newEnv(t)
	g := newGeoServer(t, []string{"x"}, []string{"y"})
	e.trust(g)
	b, _ := os.ReadFile(filepath.Join(e.bundled, RoscomGeoIP))
	g.ipSum = sha(b)
	pg := ProfileGeo{GeoIPURL: g.URL + "/geoip.dat"}
	var dials atomic.Int64
	if err := e.a.Fetch(context.Background(), pg, plainDialer(&dials)); err != nil {
		t.Fatal(err)
	}
	if g.hits.Load() != 1 || g.sumHits.Load() != 1 {
		t.Fatalf("hits=%d, want only the checksum request", g.hits.Load())
	}
}

func TestFetchUsesGivenDialer(t *testing.T) {
	e := newEnv(t)
	g := newGeoServer(t, []string{"psite"}, []string{"pip"})
	e.trust(g)
	pg := ProfileGeo{GeoIPURL: g.URL + "/geoip.dat"}
	boom := errors.New("dialer refused")
	var calls atomic.Int64
	err := e.a.Fetch(context.Background(), pg, func(ctx context.Context, network, addr string) (net.Conn, error) {
		calls.Add(1)
		return nil, boom
	})
	if err == nil || calls.Load() == 0 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	if g.hits.Load() != 0 {
		t.Fatal("server reached without the dialer")
	}
}

func TestNeedsBootstrap(t *testing.T) {
	e := newEnv(t)
	cfg := cfgFor("psite", "rip")
	pg := ProfileGeo{GeoSiteURL: "https://example.invalid/geosite.dat"}
	if !e.a.NeedsBootstrap(cfg, pg) {
		t.Fatal("want true with missing tag and geo URL")
	}
	if e.a.NeedsBootstrap(cfg, ProfileGeo{}) {
		t.Fatal("want false without geo URL")
	}
	_, missing, err := e.a.Prepare(cfg, nil)
	if err != nil || len(missing) != 1 || missing[0].Tag != "psite" {
		t.Fatalf("missing = %v err=%v", missing, err)
	}
	if e.a.NeedsBootstrap(cfgFor("rsite", "rip"), pg) {
		t.Fatal("want false when nothing is missing")
	}
}

func TestNoAutoUpdateWithoutCall(t *testing.T) {
	e := newEnv(t)
	g := newGeoServer(t, []string{"psite"}, []string{"pip"})
	e.trust(g)
	pg := ProfileGeo{GeoIPURL: g.URL + "/geoip.dat", GeoSiteURL: g.URL + "/geosite.dat"}
	e.a.NeedsBootstrap(cfgFor("psite", "pip"), pg)
	if _, _, err := e.a.Prepare(cfgFor("psite", "pip"), &pg); err != nil {
		t.Fatal(err)
	}
	if g.hits.Load() != 0 {
		t.Fatalf("Assets made %d requests by itself", g.hits.Load())
	}
}

// rawServer serves fixed bodies per path over TLS.
func rawServer(t *testing.T, h http.HandlerFunc) *geoServer {
	t.Helper()
	g := &geoServer{}
	g.Server = httptest.NewTLSServer(h)
	t.Cleanup(g.Close)
	return g
}

func TestFetchRejectsNonDatDownload(t *testing.T) {
	e := newEnv(t)
	g := rawServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("<html><body>captive portal</body></html>"))
	})
	e.trust(g)
	var dials atomic.Int64
	err := e.a.Fetch(context.Background(), ProfileGeo{GeoSiteURL: g.URL + "/geosite.dat"}, plainDialer(&dials))
	if err == nil {
		t.Fatal("HTML page accepted as geosite")
	}
	if _, err := os.Stat(filepath.Join(e.cache, "geosite.dat")); !os.IsNotExist(err) {
		t.Fatal("garbage cached")
	}
	// Empty body is not a geo file either.
	g2 := rawServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			http.NotFound(w, r)
		}
	})
	e.trust(g2)
	if err := e.a.Fetch(context.Background(), ProfileGeo{GeoIPURL: g2.URL + "/geoip.dat"}, plainDialer(&dials)); err == nil {
		t.Fatal("empty body accepted")
	}
}

func TestPrepareIgnoresCorruptCache(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.cache, 0o755); err != nil {
		t.Fatal(err)
	}
	junk := []byte("<html>not a dat</html>")
	os.WriteFile(filepath.Join(e.cache, "geosite.dat"), junk, 0o644)
	u := "https://example.invalid/geosite.dat"
	m := cacheMeta{GeoSite: &cacheEntry{URL: u, SHA256: sha(junk)}}
	if err := e.a.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	_, missing, err := e.a.Prepare(cfgFor("rsite", "rip"), &ProfileGeo{GeoSiteURL: u})
	if err != nil || len(missing) != 0 {
		t.Fatalf("prepare: %v %v", err, missing)
	}
}

func TestFetchRejectsInsecureURL(t *testing.T) {
	e := newEnv(t)
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }))
	defer plain.Close()
	var dials atomic.Int64
	err := e.a.Fetch(context.Background(), ProfileGeo{GeoIPURL: plain.URL + "/geoip.dat"}, plainDialer(&dials))
	if !errors.Is(err, ErrInsecureURL) || dials.Load() != 0 {
		t.Fatalf("err=%v dials=%d", err, dials.Load())
	}
	// A redirect from https to http is refused.
	g := rawServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
	})
	e.trust(g)
	err = e.a.Fetch(context.Background(), ProfileGeo{GeoIPURL: g.URL + "/geoip.dat"}, plainDialer(&dials))
	if err == nil || !strings.Contains(err.Error(), ErrInsecureURL.Error()) {
		t.Fatalf("redirect err = %v", err)
	}
}

func TestFetchChecksumErrors(t *testing.T) {
	e := newEnv(t)
	for name, h := range map[string]http.HandlerFunc{
		"500 on checksum": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) },
		"malformed checksum": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("not-a-hash  file"))
		},
	} {
		g := rawServer(t, h)
		e.trust(g)
		var dials atomic.Int64
		err := e.a.Fetch(context.Background(), ProfileGeo{GeoIPURL: g.URL + "/geoip.dat"}, plainDialer(&dials))
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if _, err := os.Stat(filepath.Join(e.cache, "geoip.dat")); !os.IsNotExist(err) {
			t.Fatalf("%s: cache written", name)
		}
	}
}

func TestFetchSizeCap(t *testing.T) {
	e := newEnv(t)
	e.a.maxFile = 1024 // the real cap is 64 MiB; same code path
	g := rawServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			http.NotFound(w, r)
			return
		}
		w.Write(make([]byte, 2000))
	})
	e.trust(g)
	var dials atomic.Int64
	err := e.a.Fetch(context.Background(), ProfileGeo{GeoIPURL: g.URL + "/geoip.dat"}, plainDialer(&dials))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v", err)
	}
	if maxFile != 64<<20 {
		t.Fatal("default cap changed")
	}
}

func TestAliasURLMatchesBundled(t *testing.T) {
	e := newEnv(t)
	m := map[string]PinFile{RoscomGeoSite: {URL: "https://raw.example/x", Aliases: []string{"https://cdn.example/alias.dat"}}}
	b, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(e.bundled, SourcesFile), b, 0o644)
	var dials atomic.Int64
	if err := e.a.Fetch(context.Background(), ProfileGeo{GeoSiteURL: "https://cdn.example/alias.dat"}, plainDialer(&dials)); err != nil || dials.Load() != 0 {
		t.Fatalf("err=%v dials=%d", err, dials.Load())
	}
	if e.a.NeedsBootstrap(cfgFor("psite", "rip"), ProfileGeo{GeoSiteURL: "https://cdn.example/alias.dat"}) {
		t.Fatal("bootstrap wanted for a URL that is the bundled file")
	}
}

func TestNeedsBootstrapByKind(t *testing.T) {
	e := newEnv(t)
	// Missing geosite tag, but the profile only names a geoip URL.
	if e.a.NeedsBootstrap(cfgFor("psite", "rip"), ProfileGeo{GeoIPURL: "https://example.invalid/geoip.dat"}) {
		t.Fatal("geoip URL cannot supply a geosite tag")
	}
	if e.a.NeedsBootstrap(cfgFor("psite", "rip"), ProfileGeo{GeoSiteURL: "http://example.invalid/geosite.dat"}) {
		t.Fatal("http URL cannot be fetched")
	}
}

func TestPruneOrdersByMarkerAndDropsOldTemp(t *testing.T) {
	e := newEnv(t)
	d1, _, _ := e.a.Prepare(cfgFor("rsite", "rip"), nil)
	d2, _, _ := e.a.Prepare(cfgFor("shared", "rip"), nil)
	old := filepath.Join(e.merged, ".tmp-orphan")
	fresh := filepath.Join(e.merged, ".tmp-fresh")
	os.Mkdir(old, 0o755)
	os.Mkdir(fresh, 0o755)
	past := time.Now().Add(-2 * time.Hour)
	os.Chtimes(old, past, past)
	// Make d1 look newest by dir mtime; the marker order must still win.
	future := time.Now().Add(time.Hour)
	os.Chtimes(d1, future, future)
	d3, _, err := e.a.Prepare(cfgFor("vsite", "rip"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d1); !os.IsNotExist(err) {
		t.Fatal("oldest set (by marker) survived")
	}
	for _, d := range []string{d2, d3, fresh} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("%s gone: %v", d, err)
		}
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old temp dir kept")
	}
}
