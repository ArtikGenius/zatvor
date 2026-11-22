// Command pcapcount counts leak packets in a pcapng (or classic pcap) capture
// of the physical adapter, made by pktmon during the TUN prototype's leak run
// (stage 0c, task 8).
//
//	pcapcount -probe 198.18.0.10,198.18.0.20,2001:db8::10 -probe-port 1.1.1.1:80 capture.pcapng
//
// It prints JSON: DNS packets (TCP or UDP port 53 or 853, either direction)
// per IP version, packets to or from a probe address (or probe address:port),
// unique flows of each, and samples. The upstream Xray never sends DNS on
// those ports and redirects the probe addresses to loopback, so on the
// physical adapter each such packet left outside the tunnel.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
)

// Counts is the output of pcapcount.
type Counts struct {
	Packets      int      `json:"packets"`
	IPv4         int      `json:"ipv4"`
	IPv6         int      `json:"ipv6"`
	Other        int      `json:"other"`
	DNSv4        int      `json:"dnsV4"`
	DNSv6        int      `json:"dnsV6"`
	DNSv4Flows   int      `json:"dnsV4Flows"`
	DNSv6Flows   int      `json:"dnsV6Flows"`
	Probe        int      `json:"probe"`
	ProbeFlows   int      `json:"probeFlows"`
	Samples      []string `json:"samples"`
	TopEndpoints []string `json:"topEndpoints"`
}

// Matcher says which packets count as leaks.
type Matcher struct {
	ProbeAddrs map[netip.Addr]bool
	ProbePorts map[netip.AddrPort]bool
}

// Packet is the part of a captured IP packet pcapcount looks at.
type Packet struct {
	Src, Dst     netip.Addr
	Proto        uint8
	SPort, DPort uint16
}

func (p Packet) String() string {
	return fmt.Sprintf("proto %d %s -> %s", p.Proto, netip.AddrPortFrom(p.Src, p.SPort), netip.AddrPortFrom(p.Dst, p.DPort))
}

func (p Packet) flowKey() string {
	a, b := netip.AddrPortFrom(p.Src, p.SPort).String(), netip.AddrPortFrom(p.Dst, p.DPort).String()
	if a > b {
		a, b = b, a
	}
	return fmt.Sprintf("%d %s %s", p.Proto, a, b)
}

func (p Packet) isDNS() bool {
	if p.Proto != 6 && p.Proto != 17 {
		return false
	}
	for _, port := range []uint16{p.SPort, p.DPort} {
		if port == 53 || port == 853 {
			return true
		}
	}
	return false
}

func (m Matcher) isProbe(p Packet) bool {
	if m.ProbeAddrs[p.Src] || m.ProbeAddrs[p.Dst] {
		return true
	}
	if p.Proto != 6 && p.Proto != 17 {
		return false
	}
	return m.ProbePorts[netip.AddrPortFrom(p.Src, p.SPort)] || m.ProbePorts[netip.AddrPortFrom(p.Dst, p.DPort)]
}

// Count classifies the IP packets of frames.
func Count(frames []Frame, m Matcher) Counts {
	c := Counts{Samples: []string{}, TopEndpoints: []string{}}
	dns4, dns6, probe := map[string]bool{}, map[string]bool{}, map[string]bool{}
	endpoints := map[string]int{}
	for _, f := range frames {
		c.Packets++
		p, ok := parseFrame(f)
		if !ok {
			c.Other++
			continue
		}
		if p.Src.Is4() {
			c.IPv4++
		} else {
			c.IPv6++
		}
		endpoints[fmt.Sprintf("proto %d %s", p.Proto, netip.AddrPortFrom(p.Dst, p.DPort))]++
		leak := false
		if p.isDNS() {
			leak = true
			if p.Src.Is4() {
				c.DNSv4++
				dns4[p.flowKey()] = true
			} else {
				c.DNSv6++
				dns6[p.flowKey()] = true
			}
		}
		if m.isProbe(p) {
			leak = true
			c.Probe++
			probe[p.flowKey()] = true
		}
		if leak && len(c.Samples) < 30 {
			c.Samples = append(c.Samples, p.String())
		}
	}
	c.DNSv4Flows, c.DNSv6Flows, c.ProbeFlows = len(dns4), len(dns6), len(probe)
	type kv struct {
		k string
		v int
	}
	var all []kv
	for k, v := range endpoints {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v > all[j].v || all[i].v == all[j].v && all[i].k < all[j].k })
	for i := 0; i < len(all) && i < 15; i++ {
		c.TopEndpoints = append(c.TopEndpoints, fmt.Sprintf("%d x %s", all[i].v, all[i].k))
	}
	return c
}

func parseList(s string) ([]string, error) {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out, nil
}

func main() {
	probe := flag.String("probe", "198.18.0.10,198.18.0.20,2001:db8::10", "probe addresses (comma separated)")
	probePort := flag.String("probe-port", "1.1.1.1:80", "probe address:port pairs (comma separated)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: pcapcount [flags] capture.pcapng")
		os.Exit(2)
	}
	m := Matcher{ProbeAddrs: map[netip.Addr]bool{}, ProbePorts: map[netip.AddrPort]bool{}}
	addrs, _ := parseList(*probe)
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pcapcount:", err)
			os.Exit(2)
		}
		m.ProbeAddrs[ip] = true
	}
	ports, _ := parseList(*probePort)
	for _, a := range ports {
		ap, err := netip.ParseAddrPort(a)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pcapcount:", err)
			os.Exit(2)
		}
		m.ProbePorts[ap] = true
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "pcapcount:", err)
		os.Exit(1)
	}
	frames, err := ReadCapture(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pcapcount:", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(Count(frames, m))
}
