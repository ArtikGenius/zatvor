package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
)

// Link types this reader understands.
const (
	linkEthernet = 1
	linkRaw      = 101
	linkIPv4     = 228
	linkIPv6     = 229
)

// Frame is one captured packet with the link type of its interface.
type Frame struct {
	LinkType uint16
	Data     []byte
}

// ReadCapture reads every packet of a pcapng or classic pcap file.
func ReadCapture(b []byte) ([]Frame, error) {
	if len(b) < 24 {
		return nil, errors.New("capture too short")
	}
	switch binary.LittleEndian.Uint32(b) {
	case 0x0A0D0D0A:
		return readPcapng(b)
	case 0xA1B2C3D4, 0xA1B23C4D:
		return readPcap(b, binary.LittleEndian)
	case 0xD4C3B2A1, 0x4D3CB2A1:
		return readPcap(b, binary.BigEndian)
	}
	return nil, errors.New("not a pcap or pcapng file")
}

func readPcap(b []byte, bo binary.ByteOrder) ([]Frame, error) {
	link := uint16(bo.Uint32(b[20:]))
	var out []Frame
	for off := 24; off+16 <= len(b); {
		incl := int(bo.Uint32(b[off+8:]))
		if off+16+incl > len(b) {
			return out, fmt.Errorf("truncated packet at %d", off)
		}
		out = append(out, Frame{link, b[off+16 : off+16+incl]})
		off += 16 + incl
	}
	return out, nil
}

func readPcapng(b []byte) ([]Frame, error) {
	var bo binary.ByteOrder = binary.LittleEndian
	var links []uint16
	var out []Frame
	for off := 0; off+12 <= len(b); {
		typ := bo.Uint32(b[off:])
		if typ == 0x0A0D0D0A { // section header: byte order magic decides
			if binary.LittleEndian.Uint32(b[off+8:]) == 0x1A2B3C4D {
				bo = binary.LittleEndian
			} else {
				bo = binary.BigEndian
			}
			links = nil
		}
		blen := int(bo.Uint32(b[off+4:]))
		if blen < 12 || off+blen > len(b) {
			return out, fmt.Errorf("bad block length %d at %d", blen, off)
		}
		body := b[off+8 : off+blen-4]
		switch typ {
		case 1: // interface description
			if len(body) >= 2 {
				links = append(links, bo.Uint16(body))
			}
		case 6: // enhanced packet
			if len(body) >= 20 {
				ifID := int(bo.Uint32(body))
				capLen := int(bo.Uint32(body[12:]))
				if 20+capLen <= len(body) && ifID < len(links) {
					out = append(out, Frame{links[ifID], body[20 : 20+capLen]})
				}
			}
		case 3: // simple packet: interface 0
			if len(body) >= 4 && len(links) > 0 {
				out = append(out, Frame{links[0], body[4:]})
			}
		}
		off += blen
	}
	return out, nil
}

// parseFrame finds the IP header in a frame and reads addresses and ports.
func parseFrame(f Frame) (Packet, bool) {
	d := f.Data
	switch f.LinkType {
	case linkEthernet:
		if len(d) < 14 {
			return Packet{}, false
		}
		et := binary.BigEndian.Uint16(d[12:])
		d = d[14:]
		for et == 0x8100 || et == 0x88A8 { // VLAN tags
			if len(d) < 4 {
				return Packet{}, false
			}
			et = binary.BigEndian.Uint16(d[2:])
			d = d[4:]
		}
		if et != 0x0800 && et != 0x86DD {
			return Packet{}, false
		}
	case linkRaw, linkIPv4, linkIPv6:
	default:
		return Packet{}, false
	}
	return parseIP(d)
}

func parseIP(d []byte) (Packet, bool) {
	if len(d) < 1 {
		return Packet{}, false
	}
	var p Packet
	var l4 []byte
	switch d[0] >> 4 {
	case 4:
		if len(d) < 20 {
			return Packet{}, false
		}
		ihl := int(d[0]&0x0f) * 4
		if ihl < 20 || len(d) < ihl {
			return Packet{}, false
		}
		p.Src = netip.AddrFrom4([4]byte(d[12:16]))
		p.Dst = netip.AddrFrom4([4]byte(d[16:20]))
		p.Proto = d[9]
		if binary.BigEndian.Uint16(d[6:])&0x1fff == 0 { // first fragment only has ports
			l4 = d[ihl:]
		}
	case 6:
		if len(d) < 40 {
			return Packet{}, false
		}
		p.Src = netip.AddrFrom16([16]byte(d[8:24]))
		p.Dst = netip.AddrFrom16([16]byte(d[24:40]))
		p.Proto = d[6]
		l4 = d[40:]
		// Skip the common extension headers to reach TCP/UDP.
		for p.Proto == 0 || p.Proto == 43 || p.Proto == 60 || p.Proto == 44 {
			if len(l4) < 8 {
				return p, true
			}
			next := l4[0]
			hl := (int(l4[1]) + 1) * 8
			if p.Proto == 44 {
				hl = 8
				if binary.BigEndian.Uint16(l4[2:])&0xfff8 != 0 {
					p.Proto = next
					return p, true // not the first fragment
				}
			}
			if len(l4) < hl {
				return p, true
			}
			p.Proto, l4 = next, l4[hl:]
		}
	default:
		return Packet{}, false
	}
	if (p.Proto == 6 || p.Proto == 17) && len(l4) >= 4 {
		p.SPort = binary.BigEndian.Uint16(l4)
		p.DPort = binary.BigEndian.Uint16(l4[2:])
	}
	return p, true
}
