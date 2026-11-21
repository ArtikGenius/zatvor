package main

import (
	"encoding/json"
	"testing"
)

func TestConfigHasTunKeys(t *testing.T) {
	b, err := json.Marshal(Config("zatvor-a", "127.0.0.1", 10800, `C:\Windows\System32\curl.exe`, "logs", true))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Inbounds []struct {
			Protocol string
			Settings map[string]json.RawMessage
		}
		Routing struct {
			Rules []struct {
				Process     []string
				OutboundTag string
			}
		}
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Inbounds) != 1 || c.Inbounds[0].Protocol != "tun" {
		t.Fatalf("inbounds: %+v", c.Inbounds)
	}
	for _, k := range []string{"autoSystemRoutingTable", "autoOutboundsInterface", "autoSystemWfpBlockLeak", "dns", "gateway"} {
		if _, ok := c.Inbounds[0].Settings[k]; !ok {
			t.Errorf("tun settings miss %q", k)
		}
	}
	if got := c.Routing.Rules[0]; len(got.Process) != 1 || got.Process[0] != "C:/Windows/System32/curl.exe" || got.OutboundTag != "direct" {
		t.Errorf("first rule = %+v, want curl.exe path with forward slashes to direct", got)
	}
	if n := len(c.Routing.Rules); n != 4 || c.Routing.Rules[1].OutboundTag != "direct-canon" || c.Routing.Rules[2].OutboundTag != "direct-name" {
		t.Errorf("diag rules = %+v", c.Routing.Rules)
	}
}
