//go:build xrayint

package xrayrun

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/ojson"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func mustObj(t *testing.T, js string) *ojson.Object {
	t.Helper()
	v, err := ojson.Parse([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	return v.(*ojson.Object)
}

func socksOutbound(t *testing.T, tag string, port int) *ojson.Object {
	return mustObj(t, fmt.Sprintf(`{"tag":%q,"protocol":"socks","settings":{"servers":[{"address":"127.0.0.1","port":%d}]}}`, tag, port))
}

// TestLatencyLocal: two socks outbounds, one to a live freedom-backed socks
// server (a second Xray on loopback), one to a closed port. The target is a
// local httptest server, so nothing leaves the machine.
func TestLatencyLocal(t *testing.T) {
	bin := os.Getenv("ZATVOR_XRAY")
	if bin == "" {
		t.Skip("ZATVOR_XRAY is not set")
	}
	assets := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()

	upPort, deadPort := freePort(t), freePort(t)
	cfg := fmt.Sprintf(`{"inbounds":[{"tag":"s","listen":"127.0.0.1","port":%d,"protocol":"socks","settings":{"auth":"noauth","udp":false}}],
		"outbounds":[{"tag":"direct","protocol":"freedom"}]}`, upPort)
	backend := New(bin, assets, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := backend.Start(ctx, []byte(cfg), DialReady(fmt.Sprintf("127.0.0.1:%d", upPort))); err != nil {
		t.Fatal(err)
	}
	defer backend.Stop(5 * time.Second)

	outs := []*ojson.Object{socksOutbound(t, "up", upPort), socksOutbound(t, "dead", deadPort)}
	res := Latency(ctx, bin, assets, outs, srv.URL, 10*time.Second)
	if len(res) != 2 {
		t.Fatalf("got %d results", len(res))
	}
	if res[0].Tag != "up" || res[0].Err != nil || res[0].RTT <= 0 {
		t.Errorf("up: %+v", res[0])
	}
	if res[1].Tag != "dead" || res[1].Err == nil {
		t.Errorf("dead: %+v", res[1])
	}
}

// TestLatencyFallbackPerOutbound: an outbound Xray rejects makes the shared
// run fail to start; the per-outbound fallback still measures the others,
// and only the invalid one carries an error.
func TestLatencyFallbackPerOutbound(t *testing.T) {
	bin := os.Getenv("ZATVOR_XRAY")
	if bin == "" {
		t.Skip("ZATVOR_XRAY is not set")
	}
	assets := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()

	upPort := freePort(t)
	cfg := fmt.Sprintf(`{"inbounds":[{"tag":"s","listen":"127.0.0.1","port":%d,"protocol":"socks","settings":{"auth":"noauth","udp":false}}],
		"outbounds":[{"tag":"direct","protocol":"freedom"}]}`, upPort)
	backend := New(bin, assets, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := backend.Start(ctx, []byte(cfg), DialReady(fmt.Sprintf("127.0.0.1:%d", upPort))); err != nil {
		t.Fatal(err)
	}
	defer backend.Stop(5 * time.Second)

	outs := []*ojson.Object{
		socksOutbound(t, "up1", upPort),
		mustObj(t, `{"tag":"bad","protocol":"no-such-protocol"}`),
		socksOutbound(t, "up2", upPort),
	}
	res := Latency(ctx, bin, assets, outs, srv.URL, 10*time.Second)
	if len(res) != 3 {
		t.Fatalf("got %d results", len(res))
	}
	for _, i := range []int{0, 2} {
		if res[i].Err != nil || res[i].RTT <= 0 {
			t.Errorf("%s: %+v", res[i].Tag, res[i])
		}
	}
	if res[1].Tag != "bad" || res[1].Err == nil || res[1].RTT != 0 {
		t.Errorf("bad: %+v", res[1])
	}
}
