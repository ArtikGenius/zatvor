// Package geo extracts the geosite/geoip tags an Xray config depends on.
package geo

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrExtRef is returned when a config uses an "ext:" geodata reference.
var ErrExtRef = errors.New("geo: ext: references are not supported")

// NeededTags returns the sorted, deduplicated geosite and geoip tag names
// (without prefix) referenced from routing.rules[].domain|ip and
// dns.servers[].domains|expectIPs.
func NeededTags(cfg []byte) (site []string, ip []string, err error) {
	var root map[string]any
	if err := json.Unmarshal(cfg, &root); err != nil {
		return nil, nil, fmt.Errorf("geo: parse config: %w", err)
	}
	sites, ips := map[string]struct{}{}, map[string]struct{}{}

	collect := func(obj any, keys ...string) error {
		m, ok := obj.(map[string]any)
		if !ok {
			return nil // string DNS servers etc.
		}
		for _, k := range keys {
			list, _ := m[k].([]any)
			for _, v := range list {
				s, ok := v.(string)
				if !ok {
					continue
				}
				switch {
				case strings.HasPrefix(s, "ext:"):
					return ErrExtRef
				case strings.HasPrefix(s, "geosite:"):
					sites[strings.TrimPrefix(s, "geosite:")] = struct{}{}
				case strings.HasPrefix(s, "geoip:"):
					ips[strings.TrimPrefix(s, "geoip:")] = struct{}{}
				}
			}
		}
		return nil
	}

	if r, ok := root["routing"].(map[string]any); ok {
		rules, _ := r["rules"].([]any)
		for _, rule := range rules {
			if err := collect(rule, "domain", "ip"); err != nil {
				return nil, nil, err
			}
		}
	}
	if d, ok := root["dns"].(map[string]any); ok {
		servers, _ := d["servers"].([]any)
		for _, s := range servers {
			if err := collect(s, "domains", "expectIPs"); err != nil {
				return nil, nil, err
			}
		}
	}
	return keys(sites), keys(ips), nil
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
