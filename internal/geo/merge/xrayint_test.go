//go:build xrayint

package merge

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/geo"
)

const xrayintCfg = `{
  "inbounds": [{"tag":"in","listen":"127.0.0.1","port":10808,"protocol":"socks"}],
  "outbounds": [{"tag":"direct","protocol":"freedom"},{"tag":"block","protocol":"blackhole"}],
  "routing": {"rules": [
    {"type":"field","domain":["geosite:google","geosite:win-spy","geosite:category-ru@cn"],"outboundTag":"block"},
    {"type":"field","ip":["geoip:ru","geoip:private"],"outboundTag":"direct"},
    {"type":"field","ip":["!geoip:cn","geoip:!by"],"outboundTag":"block"}
  ]}
}`

func TestOutputLoadsInXray(t *testing.T) {
	bin := os.Getenv("ZATVOR_XRAY")
	if bin == "" {
		t.Skip("ZATVOR_XRAY is not set; skipping merged-geo xray run -test")
	}
	site, ip, err := geo.NeededTags([]byte(xrayintCfg))
	if err != nil {
		t.Fatal(err)
	}
	p := writeSource(t, "roscomvpn", map[string]string{"GOOGLE": "google.example", "WIN-SPY": "spy.example"}, map[string][4]byte{"RU": {198, 18, 0, 1}, "CN": {198, 18, 0, 3}, "BY": {198, 18, 0, 4}})
	f := writeSource(t, "v2fly", map[string]string{"GOOGLE": "x.example", "CATEGORY-RU": "ru.example"}, map[string][4]byte{"PRIVATE": {198, 18, 0, 2}})
	dst := t.TempDir()
	if _, err := Merge(dst, site, ip, p, f); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "-format", "json", "-config", "stdin:", "-test")
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+dst)
	cmd.Stdin = bytes.NewReader([]byte(xrayintCfg))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("xray run -test: %v\n%s", err, out)
	}
}
