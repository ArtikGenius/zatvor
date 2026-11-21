package main

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func ipv4UDP(src, dst string, sport, dport uint16) []byte {
	b := make([]byte, 28)
	b[0] = 0x45
	b[9] = 17
	s, d := netip.MustParseAddr(src).As4(), netip.MustParseAddr(dst).As4()
	copy(b[12:], s[:])
	copy(b[16:], d[:])
	binary.BigEndian.PutUint16(b[20:], sport)
	binary.BigEndian.PutUint16(b[22:], dport)
	return b
}

func ipv6TCP(src, dst string, sport, dport uint16) []byte {
	b := make([]byte, 60)
	b[0] = 0x60
	b[6] = 6
	s, d := netip.MustParseAddr(src).As16(), netip.MustParseAddr(dst).As16()
	copy(b[8:], s[:])
	copy(b[24:], d[:])
	binary.BigEndian.PutUint16(b[40:], sport)
	binary.BigEndian.PutUint16(b[42:], dport)
	return b
}

func ether(ethertype uint16, ip []byte) []byte {
	b := make([]byte, 14, 14+len(ip))
	binary.BigEndian.PutUint16(b[12:], ethertype)
	return append(b, ip...)
}

func block(typ uint32, body []byte) []byte {
	for len(body)%4 != 0 {
		body = append(body, 0)
	}
	n := uint32(12 + len(body))
	b := binary.LittleEndian.AppendUint32(nil, typ)
	b = binary.LittleEndian.AppendUint32(b, n)
	b = append(b, body...)
	return binary.LittleEndian.AppendUint32(b, n)
}

func pcapng(frames ...[]byte) []byte {
	shb := binary.LittleEndian.AppendUint32(nil, 0x1A2B3C4D)
	shb = append(shb, 1, 0, 0, 0)
	shb = append(shb, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
	out := block(0x0A0D0D0A, shb)
	out = append(out, block(1, []byte{1, 0, 0, 0, 0, 0, 0, 0})...) // Ethernet
	for _, f := range frames {
		epb := make([]byte, 20)
		binary.LittleEndian.PutUint32(epb[12:], uint32(len(f)))
		binary.LittleEndian.PutUint32(epb[16:], uint32(len(f)))
		out = append(out, block(6, append(epb, f...))...)
	}
	return out
}

func TestCountLeaks(t *testing.T) {
	data := pcapng(
		ether(0x0800, ipv4UDP("10.1.0.4", "168.63.129.16", 50000, 53)),     // DNS v4 leak
		ether(0x0800, ipv4UDP("168.63.129.16", "10.1.0.4", 53, 50000)),     // its answer, same flow
		ether(0x86DD, ipv6TCP("2001:db8:1::4", "2001:db8::10", 50001, 80)), // probe v6
		ether(0x86DD, ipv6TCP("2001:db8:1::4", "2620:fe::fe", 50002, 853)), // DoT v6 leak
		ether(0x0800, ipv4UDP("10.1.0.4", "1.1.1.1", 50003, 443)),          // upstream DoH-ish: fine
		ether(0x0800, ipv4UDP("10.1.0.4", "1.1.1.1", 50004, 80)),           // probe port
		ether(0x0806, make([]byte, 28)),                                    // ARP
	)
	frames, err := ReadCapture(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 7 {
		t.Fatalf("frames = %d", len(frames))
	}
	m := Matcher{
		ProbeAddrs: map[netip.Addr]bool{netip.MustParseAddr("2001:db8::10"): true},
		ProbePorts: map[netip.AddrPort]bool{netip.MustParseAddrPort("1.1.1.1:80"): true},
	}
	c := Count(frames, m)
	if c.DNSv4 != 2 || c.DNSv4Flows != 1 || c.DNSv6 != 1 || c.Probe != 2 || c.ProbeFlows != 2 || c.Other != 1 || c.IPv4 != 4 || c.IPv6 != 2 {
		t.Fatalf("counts = %+v", c)
	}
}

func TestRejectsGarbage(t *testing.T) {
	if _, err := ReadCapture(make([]byte, 30)); err == nil {
		t.Fatal("want error")
	}
}
