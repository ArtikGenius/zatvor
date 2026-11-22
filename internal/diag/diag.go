// Package diag builds the diagnostics bundle that `zatvor diag` saves: a
// zip of a fixed list of files (Files) about the daemon, with every secret
// value of the stored subscriptions and profiles replaced by *** in every
// file. The API token and the discovery file are never part of it.
package diag

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/ArtikGenius/zatvor/internal/devtools"
	"github.com/ArtikGenius/zatvor/internal/engine"
	"github.com/ArtikGenius/zatvor/internal/ojson"
	"github.com/ArtikGenius/zatvor/internal/profile"
	"github.com/ArtikGenius/zatvor/internal/settings"
	"github.com/ArtikGenius/zatvor/internal/validate"
	"github.com/ArtikGenius/zatvor/internal/xraybin"
)

// Files are the names in the bundle, in this order. Nothing else is ever
// added.
var Files = []string{
	"versions.json",      // Versions
	"settings.json",      // Sources.Settings
	"journal.jsonl",      // Sources.Journal
	"zatvord.log",        // the last LogLines lines of Sources.DaemonLog
	"xray.log",           // Sources.XrayOutput
	"state.json",         // Sources.State
	"subscriptions.json", // the subscriptions' hosts only
}

// LogLines is how many of the daemon log's last lines the bundle keeps.
const LogLines = 200

// Versions are the versions the bundle reports.
type Versions struct {
	Zatvord string `json:"zatvord"`
	Xray    string `json:"xray"` // the pinned Xray-core release
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	OSBuild string `json:"osBuild,omitempty"` // Windows: major.minor.build
	Go      string `json:"go"`
}

// CurrentVersions are the versions of this process, with zatvord's own
// version as given.
func CurrentVersions(zatvord string) Versions {
	return Versions{Zatvord: zatvord, Xray: xraybin.PinnedVersion, OS: runtime.GOOS, Arch: runtime.GOARCH,
		OSBuild: osBuild(), Go: runtime.Version()}
}

// Sources are everything a bundle is made of, gathered by the daemon.
type Sources struct {
	Versions Versions
	Settings settings.Settings
	Journal  []byte // the change journal file as it is (JSON lines)
	// DaemonLog is the daemon log, oldest first; the bundle keeps its last
	// LogLines lines.
	DaemonLog []byte
	// XrayOutput is the last Xray run's output, already scrubbed of its
	// config's secrets.
	XrayOutput string
	State      engine.State
	// Subscriptions are the stored subscriptions. The bundle lists only
	// their hosts, and every secret value in them (URLs with their path
	// and query, profile IDs, credentials, keys, short IDs, server names)
	// is redacted from every file.
	Subscriptions []profile.Subscription
	Now           time.Time // the files' time in the zip
}

// Bundle writes the zip of src to w. Before that:
//   - the dev-tool journal entries' Prev and Applied values (the user's
//     own tool settings) become "***", keeping their key names (R50);
//   - the tunnel's exit addresses in the state become "***" (R51): they
//     are often the server's address;
//   - every file's final bytes are redacted with every secret of the
//     stored subscriptions, however the file encodes it (raw, JSON-escaped,
//     URL-escaped; validate.SecretVariants), and then the user info of
//     every URL becomes *** (scheme://***@host; R50), which catches the
//     user's own proxy credentials wherever they appear.
func Bundle(w io.Writer, src Sources) error {
	files, err := render(src)
	if err != nil {
		return err
	}
	scrub := validate.NewScrubber(secretsOf(src.Subscriptions))
	zw := zip.NewWriter(w)
	for _, name := range Files {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: src.Now})
		if err != nil {
			return fmt.Errorf("diag: %s: %w", name, err)
		}
		if _, err := io.WriteString(f, redactUserinfo(scrub.Scrub(string(files[name])))); err != nil {
			return fmt.Errorf("diag: %s: %w", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("diag: %w", err)
	}
	return nil
}

func render(src Sources) (map[string][]byte, error) {
	type host struct {
		Host string `json:"host"`
	}
	hosts := make([]host, 0, len(src.Subscriptions))
	for _, s := range src.Subscriptions {
		hosts = append(hosts, host{profile.URLHost(s.URL)})
	}
	st := src.State
	for _, ip := range []*string{&st.Check.IPv4, &st.Check.IPv6} {
		if *ip != "" {
			*ip = "***"
		}
	}
	files := map[string][]byte{
		"journal.jsonl": redactJournal(src.Journal),
		"zatvord.log":   tail(src.DaemonLog, LogLines),
		"xray.log":      []byte(src.XrayOutput),
	}
	for name, v := range map[string]any{
		"versions.json": src.Versions,
		"settings.json": src.Settings,
		"state.json":    st,
		"subscriptions.json": struct {
			Subscriptions []host `json:"subscriptions"`
		}{hosts},
	} {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("diag: %s: %w", name, err)
		}
		if m, changed := maskPACQueries(b); changed {
			if b, err = json.MarshalIndent(m, "", "  "); err != nil {
				return nil, fmt.Errorf("diag: %s: %w", name, err)
			}
		}
		files[name] = append(b, '\n')
	}
	return files, nil
}

// userinfoRe matches the user info of a URL: scheme://<user info>@, also
// with JSON-escaped characters in it, never across a space, slash or
// quote.
var userinfoRe = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)(?:[^\s/@"\\]|\\.)+@`)

// redactUserinfo turns scheme://user:pass@host into scheme://***@host.
func redactUserinfo(s string) string { return userinfoRe.ReplaceAllString(s, "${1}***@") }

// redactJournal replaces the values of the dev-tool entries' Prev and
// Applied with "***", keeping the key names: they are the user's own tool
// settings (their proxy, maybe with credentials), not ours. In every entry
// the query and fragment of a pac_url (a foreign PAC URL can carry a token
// there) become "?***". A line that is not a JSON object (a torn last
// line) becomes ***.
func redactJournal(journal []byte) []byte {
	if len(journal) == 0 {
		return journal
	}
	var out []byte
	for _, line := range bytes.SplitAfter(journal, []byte("\n")) {
		body := bytes.TrimSpace(line)
		if len(body) == 0 {
			continue
		}
		var e map[string]json.RawMessage
		if json.Unmarshal(body, &e) != nil {
			out = append(out, "***\n"...)
			continue
		}
		var kind string
		if json.Unmarshal(e["kind"], &kind) == nil && kind == devtools.Kind {
			for _, k := range []string{"prev", "applied"} {
				if v, ok := e[k]; ok {
					e[k] = maskValues(v)
				}
			}
			if b, err := json.Marshal(e); err == nil {
				body = b
			} else {
				body = []byte("***")
			}
		}
		if m, changed := maskPACQueries(body); changed {
			if b, err := json.Marshal(m); err == nil {
				body = b
			} else {
				body = []byte("***")
			}
		}
		out = append(append(out, body...), '\n')
	}
	return out
}

// maskPACQueries decodes raw and replaces the query and fragment of every
// string under a "pac_url" key with "?***". It reports whether anything
// changed; when nothing did, the caller keeps raw byte for byte.
func maskPACQueries(raw []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}
	changed := false
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if s, ok := e.(string); ok && k == "pac_url" {
					if i := strings.IndexAny(s, "?#"); i >= 0 {
						x[k] = s[:i] + "?***"
						changed = true
					}
					continue
				}
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return v, changed
}

// maskValues is raw with every value that is not an object, a boolean or
// null replaced by "***"; object keys stay.
func maskValues(raw json.RawMessage) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return json.RawMessage(`"***"`)
	}
	var mask func(any) any
	mask = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				x[k] = mask(e)
			}
			return x
		case bool, nil:
			return x
		default:
			return "***"
		}
	}
	b, err := json.Marshal(mask(v))
	if err != nil {
		return json.RawMessage(`"***"`)
	}
	return b
}

// tail returns the last n lines of b.
func tail(b []byte, n int) []byte {
	b = bytes.TrimRight(b, "\n")
	if len(b) == 0 {
		return nil
	}
	i := len(b)
	for ; n > 0 && i > 0; n-- {
		i = bytes.LastIndexByte(b[:i], '\n')
		if i < 0 {
			return append(b[:len(b):len(b)], '\n')
		}
	}
	return append(append([]byte(nil), b[i+1:]...), '\n')
}

// secretsOf lists the secret values of the stored subscriptions: each URL
// and its parts after the host, and each profile's ID, link, server
// address, link parameters (except plain ones such as type or security)
// and JSON config values (as validate.Scrub sees them).
func secretsOf(subs []profile.Subscription) []string {
	var out []string
	for _, s := range subs {
		out = append(out, urlSecrets(s.URL)...)
		for _, p := range s.Profiles {
			out = append(out, p.ID)
			if n := p.Node; n != nil {
				out = append(out, n.Raw, n.Address)
				if b, err := json.Marshal(n.Params); err == nil {
					out = append(out, validate.SecretValues(b)...)
				}
			}
			if p.Config != nil {
				if b, err := ojson.Marshal(p.Config); err == nil {
					out = append(out, validate.SecretValues(b)...)
				}
			}
		}
	}
	return out
}

// urlSecrets is raw and its parts that can carry a token: the user info,
// everything after the host, the path and each of its segments, the query
// and each of its pairs and values, and the fragment; escaped and
// unescaped.
func urlSecrets(raw string) []string {
	out := []string{raw}
	u, err := url.Parse(raw)
	if err != nil {
		return out
	}
	if u.Host != "" {
		if i := strings.Index(raw, u.Host); i >= 0 {
			out = append(out, raw[i+len(u.Host):])
		}
	}
	if u.User != nil {
		out = append(out, u.User.String(), u.User.Username())
		if pw, ok := u.User.Password(); ok {
			out = append(out, pw)
		}
	}
	out = append(out, u.Path, u.EscapedPath(), u.RawQuery, u.Fragment, u.EscapedFragment())
	for _, seg := range strings.Split(u.EscapedPath(), "/") {
		out = append(out, seg)
		if v, err := url.PathUnescape(seg); err == nil {
			out = append(out, v)
		}
	}
	for _, kv := range strings.Split(u.RawQuery, "&") {
		out = append(out, kv)
		if _, v, ok := strings.Cut(kv, "="); ok {
			out = append(out, v)
			if d, err := url.QueryUnescape(v); err == nil {
				out = append(out, d)
			}
		}
	}
	return out
}
