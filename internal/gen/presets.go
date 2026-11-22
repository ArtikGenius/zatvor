package gen

import (
	"fmt"

	"github.com/ArtikGenius/zatvor/internal/ojson"
	"github.com/ArtikGenius/zatvor/internal/profile"
)

// ruDirectDomains and ruDirectIPs are what the all-except-Russia preset
// sends direct.
var (
	ruDirectDomains = []string{"geosite:category-ru", "domain:ru", "domain:su", "domain:xn--p1ai"}
	ruDirectIPs     = []string{"geoip:ru", "geoip:private"}
)

// presetRouting builds the routing object of a built-in preset. Presets emit
// no dns section. aiDomains and selected feed the selected-only preset.
func presetRouting(p profile.Preset, proxy, direct string, aiDomains, selected []string) (*ojson.Object, error) {
	px, dr := target{tag: proxy}, target{tag: direct}
	var rules []any
	switch p {
	case profile.PresetAllProxy:
		rules = []any{rule("network", "tcp,udp", px)}
	case profile.PresetAllExceptRU:
		rules = []any{
			rule("domain", strList(ruDirectDomains), dr),
			rule("ip", strList(ruDirectIPs), dr),
			rule("network", "tcp,udp", px),
		}
	case profile.PresetSelectedOnly:
		sites := append(append([]string{}, aiDomains...), nonEmpty(selected)...)
		rules = []any{
			rule("domain", strList(sites), px),
			rule("network", "tcp,udp", dr),
		}
	default:
		return nil, fmt.Errorf("%w: unknown preset %q", ErrInvalidInput, p)
	}
	return kv("domainStrategy", "IPIfNonMatch", "rules", rules), nil
}
