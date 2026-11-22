//go:build xrayint

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/gen"
	"github.com/ArtikGenius/zatvor/internal/geo"
	"github.com/ArtikGenius/zatvor/internal/geo/merge"
	"github.com/ArtikGenius/zatvor/internal/geo/synth"
	"github.com/ArtikGenius/zatvor/internal/link"
	"github.com/ArtikGenius/zatvor/internal/ojson"
	"github.com/ArtikGenius/zatvor/internal/profile"
	"github.com/ArtikGenius/zatvor/internal/subbody"
	"github.com/ArtikGenius/zatvor/internal/subfetch"
	"github.com/ArtikGenius/zatvor/internal/validate"
	"github.com/ArtikGenius/zatvor/internal/xraybin"
	"github.com/ArtikGenius/zatvor/internal/xrayrun"
)

var (
	fetchOnce sync.Once
	fetchDir  string
	fetchPath string
	fetchErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if fetchDir != "" {
		os.RemoveAll(fetchDir)
	}
	os.Exit(code)
}

// xrayBin returns ZATVOR_XRAY, or fetches and verifies the pinned release for
// this platform into a temp dir once per test binary (as internal/xrayint).
func xrayBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("ZATVOR_XRAY"); p != "" {
		return p
	}
	fetchOnce.Do(func() {
		pinPath, err := xraybin.RepoPinPath()
		if err != nil {
			fetchErr = err
			return
		}
		pin, err := xraybin.LoadPin(pinPath)
		if err != nil {
			fetchErr = err
			return
		}
		fetchDir, err = os.MkdirTemp("", "zatvor-xray-")
		if err != nil {
			fetchErr = err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		platform := runtime.GOOS + "-" + runtime.GOARCH
		fetchPath, fetchErr = xraybin.Fetch(ctx, http.DefaultClient, pin, "", platform, fetchDir)
	})
	if fetchErr != nil {
		t.Fatalf("fetch xray (set ZATVOR_XRAY to skip): %v", fetchErr)
	}
	return fetchPath
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// allProxy is the routing source the protocol tests run under.
var allProxy = profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllProxy}

// runClient takes a subscription body through the whole pipeline —
// subbody.Decode, profile.Apply, gen.Build, geo merge from synthetic sources,
// validate.Schema and XrayTest — and starts the client Xray with
// xrayrun. It returns the mixed inbound's address and the generated config.
// The body must yield exactly one profile; source replaces that profile's
// routing source.
func runClient(t *testing.T, bin string, body []byte, source profile.Source) (string, []byte) {
	t.Helper()
	res := subfetch.Result{Body: subbody.Decode(body), Status: http.StatusOK, Problem: subfetch.None}
	sub, diff, err := profile.Apply(profile.Subscription{ID: "e2e"}, res, time.Now())
	if err != nil {
		t.Fatalf("profile.Apply: %v", err)
	}
	if len(sub.Profiles) != 1 || len(diff.Added) != 1 {
		t.Fatalf("profile.Apply: %d profiles, diff %+v; want exactly one new profile", len(sub.Profiles), diff)
	}
	p := sub.Profiles[0]
	p.RoutingSource = source

	ports := gen.Ports{Mixed: freeTCPPort(t), Probe: freeTCPPort(t), API: freeTCPPort(t)}
	for ports.Probe == ports.Mixed {
		ports.Probe = freeTCPPort(t)
	}
	for ports.API == ports.Mixed || ports.API == ports.Probe {
		ports.API = freeTCPPort(t)
	}
	cfg, rep, err := gen.Build(gen.Input{Profile: p, Ports: ports, ProbeUser: "e2e-probe", ProbePass: randHex(t, 12)})
	if err != nil {
		t.Fatalf("gen.Build: %v", err)
	}
	if rep.ProxyTag != "proxy" {
		t.Fatalf("gen.Build: proxy tag %q, want proxy", rep.ProxyTag)
	}

	assetDir := mergedGeo(t, cfg)
	warnings, err := validate.Schema(cfg)
	if err != nil {
		t.Fatalf("validate.Schema: %v", err)
	}
	for _, w := range warnings {
		t.Logf("schema warning: %s", w)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := validate.XrayTest(ctx, bin, cfg, assetDir); err != nil {
		t.Fatalf("validate.XrayTest: %v", err)
	}

	mixed := fmt.Sprintf("127.0.0.1:%d", ports.Mixed)
	r := xrayrun.New(bin, assetDir, nil)
	if err := r.Start(ctx, cfg, xrayrun.DialReady(mixed)); err != nil {
		t.Fatalf("start client xray: %v", err)
	}
	pid := r.Pid()
	t.Cleanup(func() {
		if err := r.Stop(5 * time.Second); err != nil {
			t.Errorf("stop client xray (pid %d): %v", pid, err)
		}
	})
	return mixed, cfg
}

// mergedGeo builds the client's asset dir the way the daemon will: the tags
// cfg needs, merged from a primary and a fallback source. Both sources are
// synthetic (the even-indexed tags in the primary, the rest in the
// fallback), so every tag is found and no real geo data is used.
func mergedGeo(t *testing.T, cfg []byte) string {
	t.Helper()
	site, ip, err := geo.NeededTags(cfg)
	if err != nil {
		t.Fatalf("geo.NeededTags: %v", err)
	}
	split := func(tags []string) (even, odd []string) {
		for i, tag := range tags {
			if i%2 == 0 {
				even = append(even, tag)
			} else {
				odd = append(odd, tag)
			}
		}
		return even, odd
	}
	pSite, fSite := split(site)
	pIP, fIP := split(ip)
	src := func(name string, site, ip []string) merge.Source {
		dir := t.TempDir()
		sp, ipp, err := synth.Write(dir, site, ip)
		if err != nil {
			t.Fatal(err)
		}
		return merge.Source{Name: name, GeoSite: sp, GeoIP: ipp}
	}
	dst := t.TempDir()
	if _, err := merge.Merge(dst, site, ip, src("primary", pSite, pIP), src("fallback", fSite, fIP)); err != nil {
		t.Fatalf("merge.Merge: %v", err)
	}
	return dst
}

// getThrough GETs http://<echo>/ip through the client's mixed inbound (as an
// HTTP proxy) and returns the address the echo saw. It retries for a short
// while: a QUIC server may need a moment after Xray reports started.
func getThrough(t *testing.T, mixed string, echo *Echo, srv *Server) string {
	t.Helper()
	proxyURL := &url.URL{Scheme: "http", Host: mixed}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true},
		Timeout:   10 * time.Second,
	}
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		resp, err := client.Get("http://" + echo.Addr + "/ip")
		if err == nil {
			b, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if rerr == nil && resp.StatusCode == http.StatusOK {
				return strings.TrimSpace(string(b))
			}
			err = fmt.Errorf("status %d, read error %v, body %q", resp.StatusCode, rerr, b)
		}
		lastErr = err
		t.Logf("GET through %s, attempt %d: %v", mixed, attempt, err)
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("GET through the client never succeeded: %v\n%s server logs:\n%s", lastErr, srv.Name, srv.Logs())
	return ""
}

// checkCarried asserts the request reached the echo through srv: the echo
// saw srv's sendThrough address (where the OS has one) and srv's access log
// records the echo destination.
func checkCarried(t *testing.T, srv *Server, echo *Echo, seen string) {
	t.Helper()
	t.Logf("echo saw %s; %s server sendThrough %q", seen, srv.Name, srv.SendThrough)
	if srv.SendThrough != "" && seen != srv.SendThrough {
		t.Errorf("echo saw %s, want %s server's outbound address %s", seen, srv.Name, srv.SendThrough)
	}
	if err := srv.WaitCarried(echo.Addr, 5*time.Second); err != nil {
		t.Errorf("%s server: %v", srv.Name, err)
	}
	if t.Failed() {
		t.Logf("%s server logs:\n%s", srv.Name, srv.Logs())
	}
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	s, err := RandomHex(n)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestE2EVLESSRealityVision(t *testing.T) {
	bin := xrayBin(t)
	keys := NewKeys(t, bin)
	echo := StartEcho(t)
	target := StartTLSEcho(t, keys)
	srv := StartReality(t, bin, keys, target.Addr)

	n := link.Node{
		Scheme:  "vless",
		Remark:  "e2e reality vision",
		Address: "127.0.0.1",
		Port:    srv.Port,
		Params: map[string]string{
			"id":         keys.UUID,
			"encryption": "none",
			"flow":       "xtls-rprx-vision",
			"security":   "reality",
			"sni":        RealitySNI,
			"fp":         "chrome",
			"pbk":        keys.RealityPublic,
			"sid":        keys.ShortID,
			"type":       "tcp",
		},
	}
	mixed, _ := runClient(t, bin, []byte(link.Export(n)+"\n"), allProxy)
	checkCarried(t, srv, echo, getThrough(t, mixed, echo, srv))
}

func TestE2EVLESSXHTTP(t *testing.T) {
	bin := xrayBin(t)
	keys := NewKeys(t, bin)
	echo := StartEcho(t)
	srv := StartXHTTP(t, bin, keys)

	n := link.Node{
		Scheme:  "vless",
		Remark:  "e2e xhttp",
		Address: "127.0.0.1",
		Port:    srv.Port,
		Params: map[string]string{
			"id":         keys.UUID,
			"encryption": "none",
			"security":   "tls",
			"sni":        XHTTPSNI,
			"pinSHA256":  keys.CertSHA256,
			"type":       "xhttp",
			"path":       XHTTPPath,
			"mode":       "packet-up",
		},
	}
	mixed, _ := runClient(t, bin, []byte(link.Export(n)+"\n"), allProxy)
	checkCarried(t, srv, echo, getThrough(t, mixed, echo, srv))
}

// hy2Fixture returns a Hysteria2-DNS fixture with its proxy outbound pointed
// at srv: address and port replaced, the auth and salamander password set to
// this run's secrets, and the server's self-signed certificate pinned (the
// fixture's serverName has no public certificate here).
func hy2Fixture(t *testing.T, name string, srv *Server, keys Keys) []byte {
	t.Helper()
	v, err := ojson.Parse(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	root := v.(*ojson.Object)
	proxy := findTagged(t, root, "outbounds", "proxy")
	settings := child(t, proxy, "settings")
	settings.Set("address", "127.0.0.1")
	settings.Set("port", json.Number(strconv.Itoa(srv.Port)))
	stream := child(t, proxy, "streamSettings")
	tls := child(t, stream, "tlsSettings")
	if sni, _ := tls.Get("serverName"); sni != Hy2SNI {
		t.Fatalf("%s: serverName %v, want %s", name, sni, Hy2SNI)
	}
	tls.Set("pinnedPeerCertSha256", keys.CertSHA256)
	child(t, stream, "hysteriaSettings").Set("auth", keys.Hy2Auth)
	fm := child(t, stream, "finalmask")
	udp, _ := fm.Get("udp")
	masks, _ := udp.([]any)
	found := false
	for _, m := range masks {
		mo, _ := m.(*ojson.Object)
		if typ, _ := mo.Get("type"); typ == "salamander" {
			child(t, mo, "settings").Set("password", keys.SalamanderPassword)
			found = true
		}
	}
	if !found {
		t.Fatalf("%s: no salamander mask", name)
	}
	out, err := ojson.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func child(t *testing.T, o *ojson.Object, key string) *ojson.Object {
	t.Helper()
	v, _ := o.Get(key)
	c, ok := v.(*ojson.Object)
	if !ok {
		t.Fatalf("no object %q", key)
	}
	return c
}

func findTagged(t *testing.T, root *ojson.Object, list, tag string) *ojson.Object {
	t.Helper()
	v, _ := root.Get(list)
	items, _ := v.([]any)
	for _, it := range items {
		o, ok := it.(*ojson.Object)
		if !ok {
			continue
		}
		if got, _ := o.Get("tag"); got == tag {
			return o
		}
	}
	t.Fatalf("no %s entry tagged %q", list, tag)
	return nil
}

func TestE2EHysteria2DNS(t *testing.T) {
	bin := xrayBin(t)
	keys := NewKeys(t, bin)
	echo := StartEcho(t)
	srv := StartHysteria2(t, bin, keys)

	body := hy2Fixture(t, "xray-hy2-dns-finalmask.json", srv, keys)
	mixed, cfg := runClient(t, bin, body, allProxy)
	if !strings.Contains(string(cfg), `"type": "noise"`) {
		t.Error("generated config lost the finalmask noise item")
	}
	checkCarried(t, srv, echo, getThrough(t, mixed, echo, srv))
}

// TestE2ELegacyRunsAsIs runs the legacy fixture (freedom noises reached via
// dialerProxy) unmigrated, with the provider's own routing and dns, against
// the Hysteria2 server (spec 6.4). The echo is on 127.0.0.1, which neither
// the synthetic geoip:private (198.18.0.0/15) nor any domain rule matches,
// so the final tcp,udp rule sends it to the proxy.
func TestE2ELegacyRunsAsIs(t *testing.T) {
	bin := xrayBin(t)
	keys := NewKeys(t, bin)
	echo := StartEcho(t)
	srv := StartHysteria2(t, bin, keys)

	body := hy2Fixture(t, "xray-hy2-dns-legacy.json", srv, keys)
	if !strings.Contains(string(body), `"dialerProxy": "dns-noise"`) || !strings.Contains(string(body), `"noises"`) {
		t.Fatal("legacy fixture no longer uses noises via dialerProxy")
	}
	mixed, cfg := runClient(t, bin, body, profile.Source{Kind: profile.SourceProvider})
	if !strings.Contains(string(cfg), `"dialerProxy": "dns-noise"`) || !strings.Contains(string(cfg), `"noises"`) {
		t.Error("generated config is not the legacy form (noises via dialerProxy)")
	}
	checkCarried(t, srv, echo, getThrough(t, mixed, echo, srv))
}

// awkward holds the characters most likely to break a link, JSON or
// command-line path: a quote, a backslash, '#', '%', a space, an emoji and
// Cyrillic.
const awkward = `a"b\c#d%e f😀Пароль`

// TestE2EAwkwardSecret takes a Hysteria2 share link whose auth, salamander
// password and remark hold awkward characters through link.Parse (via
// subbody.Decode), gen.Build, xray run -test and a real connection.
func TestE2EAwkwardSecret(t *testing.T) {
	bin := xrayBin(t)
	keys := NewKeys(t, bin)
	keys.Hy2Auth = awkward + randHex(t, 8)
	keys.SalamanderPassword = awkward + randHex(t, 8)
	echo := StartEcho(t)
	srv := StartHysteria2(t, bin, keys)

	remark := `Сервер "e2e" \ #1 100% 😀`
	q := url.Values{}
	q.Set("sni", Hy2SNI)
	q.Set("pinSHA256", keys.CertSHA256)
	q.Set("obfs", "salamander")
	q.Set("obfs-password", keys.SalamanderPassword)
	line := "hysteria2://" + url.PathEscape(keys.Hy2Auth) + "@127.0.0.1:" + strconv.Itoa(srv.Port) +
		"/?" + q.Encode() + "#" + url.PathEscape(remark)

	dec := subbody.Decode([]byte(line + "\n"))
	if len(dec.Nodes) != 1 {
		t.Fatalf("decode: %d nodes, skipped %+v", len(dec.Nodes), dec.Skipped)
	}
	n := dec.Nodes[0]
	if n.Params["password"] != keys.Hy2Auth || n.Params["obfs-password"] != keys.SalamanderPassword || n.Remark != remark {
		t.Fatal("link.Parse changed the auth, obfs password or remark")
	}
	if again, err := link.Parse(link.Export(n)); err != nil || again.Params["password"] != keys.Hy2Auth || again.Remark != remark {
		t.Fatalf("link.Export does not round-trip the awkward characters (err %v)", err)
	}

	mixed, cfg := runClient(t, bin, []byte(line+"\n"), allProxy)
	var got struct {
		Outbounds []struct {
			Tag            string
			StreamSettings struct {
				HysteriaSettings struct{ Auth string }
				Finalmask        struct {
					UDP []struct {
						Settings struct{ Password string }
					}
				}
			}
		}
	}
	if err := json.Unmarshal(cfg, &got); err != nil {
		t.Fatal(err)
	}
	ss := got.Outbounds[0].StreamSettings
	if got.Outbounds[0].Tag != "proxy" || ss.HysteriaSettings.Auth != keys.Hy2Auth ||
		len(ss.Finalmask.UDP) != 1 || ss.Finalmask.UDP[0].Settings.Password != keys.SalamanderPassword {
		t.Fatal("generated config does not carry the auth and obfs password unchanged")
	}
	checkCarried(t, srv, echo, getThrough(t, mixed, echo, srv))
}
