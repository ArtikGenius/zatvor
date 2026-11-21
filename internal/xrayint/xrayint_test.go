//go:build xrayint

package xrayint

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/gen/noisemig"
	"github.com/ArtikGenius/zatvor/internal/geo"
	"github.com/ArtikGenius/zatvor/internal/geo/synth"
	"github.com/ArtikGenius/zatvor/internal/xraybin"
)

const (
	legacyFixture    = "xray-hy2-dns-legacy.json"
	finalmaskFixture = "xray-hy2-dns-finalmask.json"
	// noiseHex is the legacy fixture's freedom noise packet, verbatim.
	noiseHex = "a3c185800001000100000000076578616d706c6503636f6d0000010001c00c000100010000105a00045db8d70e"
	// proxyDest is routed to the "proxy" outbound by the legacy fixture: an IP
	// (no DNS under IPIfNonMatch) outside the synthetic geoip ranges
	// (198.18.0.0/15), so only the final tcp,udp -> proxy rule matches.
	proxyDest = "203.0.113.10"
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
// this platform into a temp dir once per test binary.
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

func migrated(t *testing.T) []byte {
	t.Helper()
	out, rep, err := noisemig.Migrate(fixture(t, legacyFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Migrated) != 1 || rep.Migrated[0] != "proxy" || len(rep.Refused) != 0 {
		t.Fatalf("unexpected migration report %+v", rep)
	}
	return out
}

// geoDir writes synthetic geosite.dat/geoip.dat holding exactly cfg's tags.
func geoDir(t *testing.T, cfg []byte) string {
	t.Helper()
	site, ip, err := geo.NeededTags(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, _, err := synth.Write(dir, site, ip); err != nil {
		t.Fatal(err)
	}
	return dir
}

func xrayCmd(ctx context.Context, t *testing.T, cfg []byte, args ...string) *exec.Cmd {
	t.Helper()
	all := append([]string{"run", "-format", "json", "-config", "stdin:"}, args...)
	cmd := exec.CommandContext(ctx, xrayBin(t), all...)
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+geoDir(t, cfg))
	cmd.Stdin = bytes.NewReader(cfg)
	return cmd
}

// testConfig runs `xray run -test` on cfg and returns its combined output.
// Xray 26.11.9 prints its log, including deprecation warnings, to stdout.
func testConfig(t *testing.T, cfg []byte) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := xrayCmd(ctx, t, cfg, "-test").CombinedOutput()
	if err != nil {
		t.Fatalf("xray run -test: %v\n%s", err, out)
	}
	return string(out)
}

func TestXrayAcceptsLegacy(t *testing.T) {
	out := testConfig(t, fixture(t, legacyFixture))
	if !strings.Contains(out, `"noises" in Freedom is deprecated`) {
		t.Errorf("legacy: no deprecation warning in output:\n%s", out)
	}
}

func TestXrayAcceptsFinalmask(t *testing.T) {
	out := testConfig(t, fixture(t, finalmaskFixture))
	if strings.Contains(out, "deprecated") {
		t.Errorf("finalmask: unexpected deprecation warning:\n%s", out)
	}
}

func TestXrayAcceptsMigrated(t *testing.T) {
	out := testConfig(t, migrated(t))
	if strings.Contains(out, "deprecated") {
		t.Errorf("migrated: unexpected deprecation warning:\n%s", out)
	}
}

// rewriteServer points outbound tag's settings.address/port at addr
// ("host:port"). It returns nil when cfg has no such outbound or addr is bad.
func rewriteServer(cfg []byte, tag, addr string) []byte {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	var port int
	if _, err := fmt.Sscan(portStr, &port); err != nil {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(cfg, &root); err != nil {
		return nil
	}
	outs, _ := root["outbounds"].([]any)
	found := false
	for _, o := range outs {
		ob, _ := o.(map[string]any)
		if ob == nil || ob["tag"] != tag {
			continue
		}
		settings, _ := ob["settings"].(map[string]any)
		if settings == nil {
			return nil
		}
		settings["address"] = host
		settings["port"] = port
		found = true
	}
	if !found {
		return nil
	}
	b, err := json.Marshal(root)
	if err != nil {
		return nil
	}
	return b
}

// rewriteInboundPorts gives every inbound a free 127.0.0.1 port, returning
// the new config and the port chosen for inbound tag.
func rewriteInboundPorts(t *testing.T, cfg []byte, tag string) ([]byte, int) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(cfg, &root); err != nil {
		t.Fatal(err)
	}
	ins, _ := root["inbounds"].([]any)
	want := 0
	for _, in := range ins {
		ib, _ := in.(map[string]any)
		if ib == nil {
			continue
		}
		p := freeTCPPort(t)
		ib["listen"] = "127.0.0.1"
		ib["port"] = p
		if ib["tag"] == tag {
			want = p
		}
	}
	if want == 0 {
		t.Fatalf("no inbound %q", tag)
	}
	b, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return b, want
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

// socksUDPAssociate does a no-auth SOCKS5 UDP ASSOCIATE on the TCP control
// connection c and returns the relay address the server named.
func socksUDPAssociate(c net.Conn) (*net.UDPAddr, error) {
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return nil, err
	}
	r := make([]byte, 2)
	if _, err := io.ReadFull(c, r); err != nil {
		return nil, err
	}
	if r[0] != 5 || r[1] != 0 {
		return nil, fmt.Errorf("socks method reply % x", r)
	}
	if _, err := c.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return nil, err
	}
	h := make([]byte, 4)
	if _, err := io.ReadFull(c, h); err != nil {
		return nil, err
	}
	if h[1] != 0 {
		return nil, fmt.Errorf("socks UDP ASSOCIATE reply code %d", h[1])
	}
	var ip net.IP
	switch h[3] {
	case 1:
		ip = make(net.IP, 4)
	case 4:
		ip = make(net.IP, 16)
	default:
		return nil, fmt.Errorf("socks reply address type %d", h[3])
	}
	if _, err := io.ReadFull(c, ip); err != nil {
		return nil, err
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return nil, err
	}
	if ip.IsUnspecified() {
		ip = net.IPv4(127, 0, 0, 1)
	}
	return &net.UDPAddr{IP: ip, Port: int(binary.BigEndian.Uint16(pb))}, nil
}

// captureNoise runs Xray on cfg with the proxy outbound pointed at a local UDP
// sink, sends one UDP datagram through the SOCKS inbound's UDP ASSOCIATE (to
// a destination the config routes to proxy, so the hysteria2 outbound dials),
// and returns the datagrams the sink received within the capture window.
func captureNoise(t *testing.T, cfg []byte) []Datagram {
	t.Helper()
	sink, err := NewUDPSink("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	cfg = rewriteServer(cfg, "proxy", sink.Addr().String())
	if cfg == nil {
		t.Fatal("rewriteServer: no proxy outbound")
	}
	var socksTag string
	{
		var root struct {
			Inbounds []struct{ Tag, Protocol string } `json:"inbounds"`
		}
		if err := json.Unmarshal(cfg, &root); err != nil {
			t.Fatal(err)
		}
		for _, in := range root.Inbounds {
			if in.Protocol == "socks" {
				socksTag = in.Tag
			}
		}
	}
	cfg, socksPort := rewriteInboundPorts(t, cfg, socksTag)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := xrayCmd(ctx, t, cfg)
	var logBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logBuf, &logBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		t.Logf("xray log:\n%s", logBuf.String())
	}()

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	var ctl net.Conn
	for {
		ctl, err = net.DialTimeout("tcp", socksAddr, time.Second)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("socks inbound never came up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer ctl.Close()
	ctl.SetDeadline(time.Now().Add(5 * time.Second))
	relay, err := socksUDPAssociate(ctl)
	if err != nil {
		t.Fatal(err)
	}
	uc, err := net.DialUDP("udp", nil, relay)
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()
	// RSV RSV FRAG ATYP=IPv4 DST.ADDR DST.PORT DATA
	pkt := []byte{0, 0, 0, 1}
	pkt = append(pkt, net.ParseIP(proxyDest).To4()...)
	pkt = binary.BigEndian.AppendUint16(pkt, 9)
	pkt = append(pkt, []byte("zatvor-xrayint")...)
	if _, err := uc.Write(pkt); err != nil {
		t.Fatal(err)
	}
	return sink.Wait(3, 5*time.Second)
}

func describe(ds []Datagram) string {
	var b strings.Builder
	for i, d := range ds {
		n := len(d.Data)
		if n > 32 {
			n = 32
		}
		fmt.Fprintf(&b, "  #%d +%s len=%d from=%s first=%s\n", i, d.At.Sub(ds[0].At).Round(time.Microsecond), len(d.Data), d.From, hex.EncodeToString(d.Data[:n]))
	}
	return b.String()
}

func TestMigratedWireEquivalent(t *testing.T) {
	noise, err := hex.DecodeString(noiseHex)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		cfg  func(*testing.T) []byte
	}{
		{"legacy", func(t *testing.T) []byte { return fixture(t, legacyFixture) }},
		{"migrated", migrated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds := captureNoise(t, tc.cfg(t))
			t.Logf("%s: captured %d datagrams:\n%s", tc.name, len(ds), describe(ds))
			if len(ds) < 2 {
				t.Fatalf("%s: want at least 2 datagrams (noise, then QUIC), got %d", tc.name, len(ds))
			}
			if !bytes.Equal(ds[0].Data, noise) {
				t.Errorf("%s: first datagram is not the noise packet", tc.name)
			}
			if bytes.Equal(ds[1].Data, noise) {
				t.Errorf("%s: second datagram is the noise packet again", tc.name)
			}
			if ds[0].From.String() != ds[1].From.String() {
				t.Errorf("%s: noise from %s but QUIC from %s", tc.name, ds[0].From, ds[1].From)
			}
		})
	}
}
