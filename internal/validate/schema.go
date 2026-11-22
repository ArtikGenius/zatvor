// Package validate checks a generated Xray config before it replaces a running
// one: Schema walks the JSON against the pinned xray-core infra/conf types, and
// XrayTest runs the real binary with `run -test`.
package validate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/xtls/xray-core/infra/conf"
)

// outboundSettings and inboundSettings map a (lower-case) protocol name to the
// infra/conf type of its `settings` object. They mirror the pinned xray-core's
// outboundConfigLoader / inboundConfigLoader; a protocol missing here only
// produces a "not checked" warning, so Xray additions do not break users.
var outboundSettings = map[string]reflect.Type{
	"block":       reflect.TypeFor[conf.BlackholeConfig](),
	"blackhole":   reflect.TypeFor[conf.BlackholeConfig](),
	"loopback":    reflect.TypeFor[conf.LoopbackConfig](),
	"direct":      reflect.TypeFor[conf.FreedomConfig](),
	"freedom":     reflect.TypeFor[conf.FreedomConfig](),
	"http":        reflect.TypeFor[conf.HTTPClientConfig](),
	"shadowsocks": reflect.TypeFor[conf.ShadowsocksClientConfig](),
	"socks":       reflect.TypeFor[conf.SocksClientConfig](),
	"vless":       reflect.TypeFor[conf.VLessOutboundConfig](),
	"vmess":       reflect.TypeFor[conf.VMessOutboundConfig](),
	"trojan":      reflect.TypeFor[conf.TrojanClientConfig](),
	"hysteria":    reflect.TypeFor[conf.HysteriaClientConfig](),
	"masque":      reflect.TypeFor[conf.MasqueClientConfig](),
	"dns":         reflect.TypeFor[conf.DNSOutboundConfig](),
	"wireguard":   reflect.TypeFor[conf.WireGuardConfig](),
}

var inboundSettings = map[string]reflect.Type{
	"tunnel":        reflect.TypeFor[conf.DokodemoConfig](),
	"dokodemo-door": reflect.TypeFor[conf.DokodemoConfig](),
	"http":          reflect.TypeFor[conf.HTTPServerConfig](),
	"shadowsocks":   reflect.TypeFor[conf.ShadowsocksServerConfig](),
	"mixed":         reflect.TypeFor[conf.SocksServerConfig](),
	"socks":         reflect.TypeFor[conf.SocksServerConfig](),
	"vless":         reflect.TypeFor[conf.VLessInboundConfig](),
	"vmess":         reflect.TypeFor[conf.VMessInboundConfig](),
	"trojan":        reflect.TypeFor[conf.TrojanServerConfig](),
	"wireguard":     reflect.TypeFor[conf.WireGuardConfig](),
	"hysteria":      reflect.TypeFor[conf.HysteriaServerConfig](),
	"masque":        reflect.TypeFor[conf.MasqueServerConfig](),
	"tun":           reflect.TypeFor[conf.TunConfig](),
}

// knownNetworks and knownSecurity are the streamSettings network/security
// names the pinned Xray still accepts (removed ones such as quic/h2/xtls are
// absent, so they surface as warnings; Xray itself rejects them).
var knownNetworks = setOf("raw", "tcp", "xhttp", "splithttp", "kcp", "mkcp", "grpc", "ws", "websocket", "httpupgrade", "hysteria", "masque", "xdrive")
var knownSecurity = setOf("none", "tls", "reality")

// extraRootKeys are root keys Xray accepts but infra/conf does not model.
var extraRootKeys = setOf("remarks")

func setOf(s ...string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

type walker struct {
	warnings []string
	err      error
}

func (w *walker) warn(format string, a ...any) {
	w.warnings = append(w.warnings, fmt.Sprintf(format, a...))
}

func (w *walker) fail(path string, format string, a ...any) {
	if w.err == nil {
		w.err = &SchemaError{Path: path, Msg: fmt.Sprintf(format, a...)}
	}
}

// SchemaError is a config that does not match the Xray schema. Path is the
// JSON path of the bad value ("outbounds[0].settings.port"), empty when the
// document itself is not valid JSON.
type SchemaError struct {
	Path string
	Msg  string
}

func (e *SchemaError) Error() string {
	if e.Path == "" {
		return e.Msg
	}
	return e.Path + ": " + e.Msg
}

// Schema checks cfg against the xray-core infra/conf types. Unknown keys and
// unchecked protocols are returned as warnings; malformed JSON or a value of
// the wrong type is a *SchemaError naming the JSON path.
func Schema(cfg []byte) (warnings []string, err error) {
	var root json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(cfg))
	if err := dec.Decode(&root); err != nil {
		return nil, &SchemaError{Msg: "config is not valid JSON: " + err.Error()}
	}
	if dec.More() {
		return nil, &SchemaError{Msg: "config is not valid JSON: trailing data"}
	}
	w := &walker{}
	w.walk("", root, reflect.TypeFor[conf.Config](), true)
	if w.err != nil {
		return nil, w.err
	}
	w.dispatch(root)
	if w.err != nil {
		return nil, w.err
	}
	return w.warnings, nil
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

var unmarshalerType = reflect.TypeFor[json.Unmarshaler]()

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// walk validates raw against t. root enables the extra root keys.
func (w *walker) walk(path string, raw json.RawMessage, t reflect.Type, root bool) {
	if w.err != nil || isNull(raw) {
		return
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeFor[json.RawMessage]() || t.Kind() == reflect.Interface {
		return
	}
	// Types with their own decoder accept several JSON shapes: do not descend,
	// decode into a fresh instance instead.
	if t.Implements(unmarshalerType) || reflect.PointerTo(t).Implements(unmarshalerType) {
		w.decodeInto(path, raw, t)
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			w.fail(path, "expected an object")
			return
		}
		fields := fieldIndex(t)
		keys := sortedKeys(obj)
		for _, k := range keys {
			ft, ok := fields[strings.ToLower(k)]
			if !ok {
				if root && extraRootKeys[strings.ToLower(k)] {
					continue
				}
				w.warn("%s: unknown key", join(path, k))
				continue
			}
			w.walk(join(path, k), obj[k], ft, false)
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			w.decodeInto(path, raw, t)
			return
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			w.fail(path, "expected an array")
			return
		}
		for i, e := range arr {
			w.walk(fmt.Sprintf("%s[%d]", path, i), e, t.Elem(), false)
		}
	case reflect.Map:
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			w.fail(path, "expected an object")
			return
		}
		for _, k := range sortedKeys(obj) {
			w.walk(join(path, k), obj[k], t.Elem(), false)
		}
	default:
		w.decodeInto(path, raw, t)
	}
}

func (w *walker) decodeInto(path string, raw json.RawMessage, t reflect.Type) {
	if err := json.Unmarshal(raw, reflect.New(t).Interface()); err != nil {
		w.fail(path, "wrong type or value (%s): %v", t, err)
	}
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fieldIndex maps lower-cased JSON key to field type, flattening embedded
// structs and skipping unexported and `json:"-"` fields.
func fieldIndex(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	collectFields(t, out)
	return out
}

func collectFields(t reflect.Type, out map[string]reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				collectFields(et, out)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		key := strings.ToLower(name)
		if _, dup := out[key]; !dup {
			out[key] = f.Type
		}
	}
}

// dispatch checks what infra/conf keeps as raw JSON: each inbound/outbound
// `settings` by protocol, and streamSettings network/security names.
func (w *walker) dispatch(root json.RawMessage) {
	var top map[string]json.RawMessage
	if json.Unmarshal(root, &top) != nil {
		return
	}
	for _, side := range []struct {
		key  string
		maps map[string]reflect.Type
	}{{"inbounds", inboundSettings}, {"outbounds", outboundSettings}} {
		raw, ok := lookup(top, side.key)
		if !ok {
			continue
		}
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil {
			continue
		}
		for i, item := range arr {
			var obj map[string]json.RawMessage
			if json.Unmarshal(item, &obj) != nil {
				continue
			}
			path := fmt.Sprintf("%s[%d]", side.key, i)
			w.checkStream(path, obj)
			var protocol string
			if p, ok := lookup(obj, "protocol"); ok {
				_ = json.Unmarshal(p, &protocol)
			}
			protocol = strings.ToLower(protocol)
			if protocol == "" {
				continue
			}
			t, known := side.maps[protocol]
			if !known {
				w.warn("%s: protocol %q not checked", path, protocol)
				continue
			}
			if s, ok := lookup(obj, "settings"); ok {
				w.walk(join(path, "settings"), s, t, false)
			}
		}
	}
}

func (w *walker) checkStream(path string, obj map[string]json.RawMessage) {
	raw, ok := lookup(obj, "streamSettings")
	if !ok {
		return
	}
	var ss map[string]json.RawMessage
	if json.Unmarshal(raw, &ss) != nil {
		return
	}
	for _, c := range []struct {
		key   string
		known map[string]bool
	}{{"network", knownNetworks}, {"security", knownSecurity}} {
		var v string
		if r, ok := lookup(ss, c.key); ok && json.Unmarshal(r, &v) == nil && v != "" && !c.known[strings.ToLower(v)] {
			w.warn("%s.%s: %s %q not checked", join(path, "streamSettings"), c.key, c.key, v)
		}
	}
}

// lookup finds key case-insensitively.
func lookup(m map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}
