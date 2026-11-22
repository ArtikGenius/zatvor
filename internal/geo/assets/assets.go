// Package assets owns the geo data Xray runs with: the bundled, pinned dat
// files, the per-profile downloads cached next to them, and the merged
// geosite.dat/geoip.dat sets built from both.
//
// Nothing here talks to the network on its own. Only Fetch does, and only
// when the caller invokes it, through the dialer the caller supplies.
package assets

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ArtikGenius/zatvor/internal/geo"
	"github.com/ArtikGenius/zatvor/internal/geo/merge"
	"github.com/ArtikGenius/zatvor/internal/subfetch"
	"github.com/xtls/xray-core/common/geodata"
	"google.golang.org/protobuf/proto"
)

const (
	markerName = ".complete"
	metaName   = "meta.json"
	keepSets   = 2
	maxFile    = 64 << 20 // default cap per downloaded file
	tmpMaxAge  = time.Hour
)

// ProfileGeo holds the geo file URLs a profile names (Happ Geoipurl and
// Geositeurl). An empty URL means the profile names none for that kind.
type ProfileGeo struct {
	GeoIPURL, GeoSiteURL string
}

func (p ProfileGeo) any() bool { return p.GeoIPURL != "" || p.GeoSiteURL != "" }

// Assets prepares merged geo sets.
type Assets struct {
	bundled, cache, merged string
	maxFile                int64
	tlsConfig              *tls.Config // nil = system roots; tests set it
	mu                     sync.Mutex
}

// New returns Assets reading the bundled files from bundledDir (as written by
// tools/geofetch), caching profile downloads in cacheDir and building merged
// sets under mergedRoot.
func New(bundledDir, cacheDir, mergedRoot string) *Assets {
	return &Assets{bundled: bundledDir, cache: cacheDir, merged: mergedRoot, maxFile: maxFile}
}

// cacheEntry describes one cached profile file.
type cacheEntry struct {
	URL string `json:"url"`
	// SHA256 is the hash of the cached bytes.
	SHA256 string `json:"sha256"`
	// ChecksumPublished is false when the server had no .sha256 file and the
	// download relied on TLS alone.
	ChecksumPublished bool      `json:"checksumPublished"`
	FetchedAt         time.Time `json:"fetchedAt"`
}

type cacheMeta struct {
	GeoIP   *cacheEntry `json:"geoip,omitempty"`
	GeoSite *cacheEntry `json:"geosite,omitempty"`
}

func (a *Assets) loadMeta() cacheMeta {
	var m cacheMeta
	if b, err := os.ReadFile(filepath.Join(a.cache, metaName)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// cachedPath returns the cache file for kind when the metadata names url and
// the file still has the recorded hash.
func (a *Assets) cachedPath(kind, url string, e *cacheEntry) (path, sum string) {
	if url == "" || e == nil || e.URL != url {
		return "", ""
	}
	p := filepath.Join(a.cache, kind+".dat")
	b, err := os.ReadFile(p)
	if err != nil {
		return "", ""
	}
	s := sha(b)
	// A cached file that no longer matches its record or does not parse is
	// ignored; bundled data serves instead.
	if s != e.SHA256 || validDat(b, kind == "geosite") != nil {
		return "", ""
	}
	return p, s
}

// inputs resolves the four files a merge uses and their hashes.
type inputs struct {
	primary, fallback merge.Source
	sums              []string // geosite primary, geoip primary, v2fly geosite, v2fly geoip
}

func (a *Assets) resolve(pg *ProfileGeo) (inputs, error) {
	var in inputs
	in.primary = merge.Source{Name: "roscomvpn",
		GeoIP: filepath.Join(a.bundled, RoscomGeoIP), GeoSite: filepath.Join(a.bundled, RoscomGeoSite)}
	in.fallback = merge.Source{Name: "v2fly",
		GeoIP: filepath.Join(a.bundled, V2flyGeoIP), GeoSite: filepath.Join(a.bundled, V2flyGeoSite)}
	if pg != nil {
		meta := a.loadMeta()
		if p, _ := a.cachedPath("geoip", pg.GeoIPURL, meta.GeoIP); p != "" {
			in.primary.GeoIP = p
		}
		if p, _ := a.cachedPath("geosite", pg.GeoSiteURL, meta.GeoSite); p != "" {
			in.primary.GeoSite = p
		}
	}
	for _, p := range []string{in.primary.GeoSite, in.primary.GeoIP, in.fallback.GeoSite, in.fallback.GeoIP} {
		s, err := fileSHA(p)
		if err != nil {
			return in, fmt.Errorf("geo assets: %w", err)
		}
		in.sums = append(in.sums, s)
	}
	return in, nil
}

// tagKey normalizes a tag the way merge does.
func tagKey(t string) string {
	base, _, _ := strings.Cut(strings.TrimLeft(t, "!"), "@")
	return strings.ToUpper(base)
}

func codes(path string, site bool) (map[string]bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	if site {
		var l geodata.GeoSiteList
		if err := proto.Unmarshal(b, &l); err != nil {
			return nil, err
		}
		for _, e := range l.Entry {
			out[strings.ToUpper(e.Code)] = true
		}
		return out, nil
	}
	var l geodata.GeoIPList
	if err := proto.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	for _, e := range l.Entry {
		out[strings.ToUpper(e.Code)] = true
	}
	return out, nil
}

// split separates the tags no source has from those that can be merged.
func split(kind string, tags []string, paths []string, site bool, searched []string) (ok []string, missing []merge.MissingTagError, err error) {
	have := map[string]bool{}
	for _, p := range paths {
		c, err := codes(p, site)
		if err != nil {
			return nil, nil, fmt.Errorf("geo assets: parse %s: %w", filepath.Base(p), err)
		}
		for k := range c {
			have[k] = true
		}
	}
	for _, t := range tags {
		k := tagKey(t)
		if k == "" {
			continue
		}
		if have[k] {
			ok = append(ok, t)
			continue
		}
		base, _, _ := strings.Cut(strings.TrimLeft(t, "!"), "@")
		missing = append(missing, merge.MissingTagError{Tag: base, Kind: kind, Searched: searched})
	}
	return ok, missing, nil
}

func (a *Assets) plan(cfg []byte, pg *ProfileGeo) (in inputs, site, ip []string, missing []merge.MissingTagError, allSite, allIP []string, err error) {
	allSite, allIP, err = geo.NeededTags(cfg)
	if err != nil {
		return
	}
	if in, err = a.resolve(pg); err != nil {
		return
	}
	searched := []string{in.primary.Name, in.fallback.Name}
	var m1, m2 []merge.MissingTagError
	site, m1, err = split("geosite", allSite, []string{in.primary.GeoSite, in.fallback.GeoSite}, true, searched)
	if err != nil {
		return
	}
	ip, m2, err = split("geoip", allIP, []string{in.primary.GeoIP, in.fallback.GeoIP}, false, searched)
	missing = append(m1, m2...)
	return
}

func setKey(allSite, allIP, sums []string) string {
	s := append([]string(nil), allSite...)
	i := append([]string(nil), allIP...)
	sort.Strings(s)
	sort.Strings(i)
	h := sha256.New()
	w := func(prefix string, vs []string) {
		for _, v := range vs {
			fmt.Fprintf(h, "%s:%d:%s\n", prefix, len(v), v)
		}
	}
	w("site", s)
	w("ip", i)
	w("file", sums)
	return hex.EncodeToString(h.Sum(nil))
}

// Prepare returns a directory holding geosite.dat and geoip.dat with exactly
// the tags cfg needs. A nil profileGeo uses the bundled data only. A
// profile's own file replaces the bundled roscomvpn one once it is in the
// cache; until then bundled data is used. Tags no source has are returned in
// missing and left out of the set.
//
// Sets live in mergedRoot/<key>. A set is complete only with its marker file,
// which is written last; the set is built in a temp dir and renamed into
// place. The newest two complete sets are kept.
func (a *Assets) Prepare(cfg []byte, profileGeo *ProfileGeo) (assetDir string, missing []merge.MissingTagError, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	in, site, ip, missing, allSite, allIP, err := a.plan(cfg, profileGeo)
	if err != nil {
		return "", nil, err
	}
	key := setKey(allSite, allIP, in.sums)
	dir := filepath.Join(a.merged, key)
	if _, err := os.Stat(filepath.Join(dir, markerName)); err == nil {
		_ = writeAtomic(filepath.Join(dir, markerName), a.marker())
		a.prune(key)
		return dir, missing, nil
	}
	if err := os.MkdirAll(a.merged, 0o755); err != nil {
		return "", nil, err
	}
	tmp, err := os.MkdirTemp(a.merged, ".tmp-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(tmp)
	if _, err := merge.Merge(tmp, site, ip, in.primary, in.fallback); err != nil {
		return "", nil, err
	}
	if err := syncDirFiles(tmp, "geosite.dat", "geoip.dat"); err != nil {
		return "", nil, err
	}
	if err := writeMarker(filepath.Join(tmp, markerName), a.marker()); err != nil {
		return "", nil, err
	}
	// An incomplete dir under this key is garbage: replace it.
	if err := os.RemoveAll(dir); err != nil {
		return "", nil, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return "", nil, fmt.Errorf("geo assets: %w", err)
	}
	syncDir(a.merged)
	a.prune(key)
	return dir, missing, nil
}

func syncDirFiles(dir string, names ...string) error {
	for _, n := range names {
		f, err := os.OpenFile(filepath.Join(dir, n), os.O_RDWR, 0)
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeMarker(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

type markerData struct {
	Seq int64 `json:"seq"`
}

func markerSeq(dir string) (int64, bool) {
	b, err := os.ReadFile(filepath.Join(dir, markerName))
	if err != nil {
		return 0, false
	}
	var m markerData
	_ = json.Unmarshal(b, &m) // an unreadable marker still marks completeness, as oldest
	return m.Seq, true
}

// marker returns marker content with a sequence number above every existing
// set's and the clock's, so ordering never depends on directory mtimes.
func (a *Assets) marker() []byte {
	seq := time.Now().UnixNano()
	if ents, err := os.ReadDir(a.merged); err == nil {
		for _, e := range ents {
			if n, ok := markerSeq(filepath.Join(a.merged, e.Name())); ok && n >= seq {
				seq = n + 1
			}
		}
	}
	b, _ := json.Marshal(markerData{Seq: seq})
	return b
}

// syncDir flushes directory entries where the platform allows it.
func syncDir(dir string) {
	if f, err := os.Open(dir); err == nil {
		_ = f.Sync()
		f.Close()
	}
}

// prune removes incomplete sets, orphaned temp dirs older than tmpMaxAge and
// all but the newest keepSets complete sets (by marker sequence). The set
// named keep is never removed.
func (a *Assets) prune(keep string) {
	ents, err := os.ReadDir(a.merged)
	if err != nil {
		return
	}
	type set struct {
		name string
		seq  int64
	}
	var complete []set
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(a.merged, e.Name())
		if strings.HasPrefix(e.Name(), ".tmp-") {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > tmpMaxAge {
				os.RemoveAll(p)
			}
			continue
		}
		seq, ok := markerSeq(p)
		if !ok {
			if e.Name() != keep {
				os.RemoveAll(p)
			}
			continue
		}
		complete = append(complete, set{e.Name(), seq})
	}
	sort.Slice(complete, func(i, j int) bool { return complete[i].seq > complete[j].seq })
	for i, s := range complete {
		if i >= keepSets && s.name != keep {
			os.RemoveAll(filepath.Join(a.merged, s.name))
		}
	}
}

// NeedsBootstrap reports that tags cfg needs are missing from the bundled and
// cached data while the profile names an https geo URL of that kind which is
// not already cached or bundled and so could supply them. The caller then
// fetches through the tunnel (spec §16 Q2.10).
func (a *Assets) NeedsBootstrap(cfg []byte, pg ProfileGeo) bool {
	if !pg.any() {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, _, _, missing, _, _, err := a.plan(cfg, &pg)
	if err != nil {
		return false
	}
	meta := a.loadMeta()
	usable := func(kind, u string, e *cacheEntry) bool {
		if u == "" || checkHTTPS(u) != nil || a.bundledURLs()[u] {
			return false
		}
		p, _ := a.cachedPath(kind, u, e)
		return p == ""
	}
	for _, m := range missing {
		if m.Kind == "geosite" && usable("geosite", pg.GeoSiteURL, meta.GeoSite) {
			return true
		}
		if m.Kind == "geoip" && usable("geoip", pg.GeoIPURL, meta.GeoIP) {
			return true
		}
	}
	return false
}

var errNotFound = errors.New("not found")

// ErrInsecureURL is returned for a profile geo URL that is not https.
var ErrInsecureURL = errors.New("geo assets: profile geo URL must be https")

func checkHTTPS(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ErrInsecureURL
	}
	return nil
}

// validDat reports whether b parses as a non-empty geosite (site) or geoip
// list. A captive-portal page or truncated file fails here.
func validDat(b []byte, site bool) error {
	n := 0
	if site {
		var l geodata.GeoSiteList
		if err := proto.Unmarshal(b, &l); err != nil {
			return err
		}
		n = len(l.Entry)
	} else {
		var l geodata.GeoIPList
		if err := proto.Unmarshal(b, &l); err != nil {
			return err
		}
		n = len(l.Entry)
	}
	if n == 0 {
		return errors.New("no entries")
	}
	return nil
}

// result of the verification phase for one kind.
type fetched struct {
	kind  string
	drop  bool // bundled data serves: forget the cached file
	body  []byte
	entry *cacheEntry
}

// Fetch downloads the geo files pg names into the cache, through dial only.
// Every URL must be https. A file is skipped when its URL is already cached,
// equals a bundled file's pinned URL (or alias), or its published SHA-256
// equals a bundled file's. A published "<url>.sha256" must match the
// download; a 404 for it means none is published and only TLS protects the
// file (recorded in the cache metadata). A download must parse as a geo dat.
// Both files are downloaded and verified before either is swapped into the
// cache, so a failure leaves the cache untouched.
func (a *Assets) Fetch(ctx context.Context, pg ProfileGeo, dial subfetch.Dialer) error {
	for _, u := range []string{pg.GeoIPURL, pg.GeoSiteURL} {
		if u != "" {
			if err := checkHTTPS(u); err != nil {
				return err
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dial,
			TLSClientConfig:       a.tlsConfig,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return ErrInsecureURL
			}
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	defer client.CloseIdleConnections()
	if err := os.MkdirAll(a.cache, 0o755); err != nil {
		return err
	}
	meta := a.loadMeta()
	var work []fetched
	for _, k := range []struct {
		kind, url string
		cur       *cacheEntry
		bundled   []string
	}{
		{"geoip", pg.GeoIPURL, meta.GeoIP, []string{RoscomGeoIP, V2flyGeoIP}},
		{"geosite", pg.GeoSiteURL, meta.GeoSite, []string{RoscomGeoSite, V2flyGeoSite}},
	} {
		if k.url == "" {
			continue
		}
		f, err := a.verify(ctx, client, k.kind, k.url, k.cur, k.bundled)
		if err != nil {
			return fmt.Errorf("geo assets: %s: %w", k.kind, err)
		}
		if f != nil {
			work = append(work, *f)
		}
	}
	changed := false
	for _, f := range work {
		cur := &meta.GeoIP
		if f.kind == "geosite" {
			cur = &meta.GeoSite
		}
		if f.drop {
			if *cur != nil {
				*cur = nil
				changed = true
			}
			continue
		}
		if err := writeAtomic(filepath.Join(a.cache, f.kind+".dat"), f.body); err != nil {
			return err
		}
		*cur = f.entry
		changed = true
	}
	if changed {
		return a.saveMeta(meta)
	}
	return nil
}

func (a *Assets) bundledURLs() map[string]bool {
	out := map[string]bool{}
	var src map[string]PinFile
	if b, err := os.ReadFile(filepath.Join(a.bundled, SourcesFile)); err == nil {
		_ = json.Unmarshal(b, &src)
	}
	for _, p := range src {
		out[p.URL] = true
		for _, al := range p.Aliases {
			out[al] = true
		}
	}
	return out
}

// verify downloads and checks one file. It returns nil when nothing needs to
// change.
func (a *Assets) verify(ctx context.Context, c *http.Client, kind, url string, cur *cacheEntry, bundled []string) (*fetched, error) {
	if p, _ := a.cachedPath(kind, url, cur); p != "" {
		return nil, nil
	}
	drop := &fetched{kind: kind, drop: true}
	if a.bundledURLs()[url] {
		return drop, nil
	}

	want, err := get(ctx, c, url+".sha256", 4096)
	published := true
	var wantSum string
	switch {
	case errors.Is(err, errNotFound):
		published = false
	case err != nil:
		return nil, fmt.Errorf("checksum: %w", err)
	default:
		wantSum = firstHex(string(want))
		if wantSum == "" {
			return nil, errors.New("checksum file is malformed")
		}
		for _, n := range bundled {
			if s, err := fileSHA(filepath.Join(a.bundled, n)); err == nil && s == wantSum {
				return drop, nil
			}
		}
	}

	body, err := get(ctx, c, url, a.maxFile)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return nil, errors.New("file not found (HTTP 404)")
		}
		return nil, err
	}
	got := sha(body)
	if published && got != wantSum {
		return nil, errors.New("SHA-256 mismatch with published checksum")
	}
	if err := validDat(body, kind == "geosite"); err != nil {
		return nil, fmt.Errorf("not a geo data file: %w", err)
	}
	return &fetched{kind: kind, body: body,
		entry: &cacheEntry{URL: url, SHA256: got, ChecksumPublished: published, FetchedAt: time.Now().UTC()}}, nil
}

func (a *Assets) saveMeta(m cacheMeta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(a.cache, metaName), b)
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func firstHex(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 || len(f[0]) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(f[0]); err != nil {
		return ""
	}
	return strings.ToLower(f[0])
}

// get reads a URL up to limit bytes. A 404 returns errNotFound (the caller
// decides what a missing resource means); any other non-200 is an error.
func get(ctx context.Context, c *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", stripURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return b, nil
}

// stripURL drops the URL from a *url.Error so it never reaches logs.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		os.Remove(name)
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}
