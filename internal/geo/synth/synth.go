// Package synth writes tiny synthetic geosite.dat / geoip.dat files for tests,
// containing exactly the requested tags and nothing real.
package synth

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/xtls/xray-core/common/geodata"
	"google.golang.org/protobuf/proto"
)

// Write creates geosite.dat and geoip.dat in dir. Each site tag t gets one
// domain entry "t.zatvor-test.invalid"; each ip tag gets one /32 from
// 198.18.0.0/15 chosen by its index. Codes are stored upper-cased, as Xray's
// loader looks them up.
func Write(dir string, site, ip []string) (sitePath, ipPath string, err error) {
	sl := &geodata.GeoSiteList{}
	for _, t := range site {
		sl.Entry = append(sl.Entry, &geodata.GeoSite{
			Code: strings.ToUpper(t),
			Domain: []*geodata.Domain{{
				Type:  geodata.Domain_Domain,
				Value: t + ".zatvor-test.invalid",
			}},
		})
	}

	base := netip.MustParseAddr("198.18.0.0")
	il := &geodata.GeoIPList{}
	for i, t := range ip {
		if i >= 1<<17 { // 198.18.0.0/15 holds 2^17 addresses
			return "", "", fmt.Errorf("synth: too many ip tags (%d)", len(ip))
		}
		n := binary.BigEndian.Uint32(base.AsSlice()) + uint32(i)
		a := binary.BigEndian.AppendUint32(nil, n)
		il.Entry = append(il.Entry, &geodata.GeoIP{
			Code: strings.ToUpper(t),
			Cidr: []*geodata.CIDR{{Ip: a, Prefix: 32}},
		})
	}

	sitePath = filepath.Join(dir, "geosite.dat")
	ipPath = filepath.Join(dir, "geoip.dat")
	if err = writePB(sitePath, sl); err != nil {
		return "", "", err
	}
	if err = writePB(ipPath, il); err != nil {
		return "", "", err
	}
	return sitePath, ipPath, nil
}

func writePB(path string, m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
