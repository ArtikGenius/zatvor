// Package merge builds one geosite.dat and one geoip.dat holding exactly the
// tags a config needs, taking each tag from the primary source (roscomvpn)
// and falling back to the second (v2fly).
package merge

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xtls/xray-core/common/geodata"
	"google.golang.org/protobuf/proto"
)

// Source is a named pair of dat files.
type Source struct {
	Name, GeoIP, GeoSite string
}

// Result names the merged files and where each tag came from. From is keyed
// "geosite:<tag>" or "geoip:<tag>" (the tag as requested, attribute suffix
// removed), because geosite and geoip tags can share a name.
type Result struct {
	GeoIP, GeoSite string
	From           map[string]string
}

// MissingTagError reports a needed tag that no searched source has.
type MissingTagError struct {
	Tag, Kind string // Kind is "geosite" or "geoip"
	Searched  []string
}

func (e *MissingTagError) Error() string {
	return fmt.Sprintf("%s tag %q is missing from %s", e.Kind, e.Tag, strings.Join(e.Searched, " and "))
}

// afterTemps is a test hook: it runs once both temp files are written and
// before either is renamed into place. A non-nil error aborts the merge.
var afterTemps func() error

// Merge writes geosite.dat and geoip.dat into dstDir containing only the
// requested tags. Tags match case-insensitively and a leading "!" and a
// "@attr" suffix are ignored; the output keeps the source's own spelling. Existing output is
// replaced only after both new files are complete.
func Merge(dstDir string, site, ip []string, primary, fallback Source) (Result, error) {
	res := Result{
		GeoSite: filepath.Join(dstDir, "geosite.dat"),
		GeoIP:   filepath.Join(dstDir, "geoip.dat"),
		From:    map[string]string{},
	}
	srcs := []Source{primary, fallback}
	names := []string{primary.Name, fallback.Name}

	siteOut, err := pick("geosite", site, srcs, names, res.From,
		func(s Source) string { return s.GeoSite },
		func(b []byte) ([]entry, error) {
			var l geodata.GeoSiteList
			if err := proto.Unmarshal(b, &l); err != nil {
				return nil, err
			}
			es := make([]entry, len(l.Entry))
			for i, e := range l.Entry {
				es[i] = entry{e.Code, e}
			}
			return es, nil
		})
	if err != nil {
		return Result{}, err
	}
	ipOut, err := pick("geoip", ip, srcs, names, res.From,
		func(s Source) string { return s.GeoIP },
		func(b []byte) ([]entry, error) {
			var l geodata.GeoIPList
			if err := proto.Unmarshal(b, &l); err != nil {
				return nil, err
			}
			es := make([]entry, len(l.Entry))
			for i, e := range l.Entry {
				es[i] = entry{e.Code, e}
			}
			return es, nil
		})
	if err != nil {
		return Result{}, err
	}

	sl := &geodata.GeoSiteList{}
	for _, m := range siteOut {
		sl.Entry = append(sl.Entry, m.(*geodata.GeoSite))
	}
	il := &geodata.GeoIPList{}
	for _, m := range ipOut {
		il.Entry = append(il.Entry, m.(*geodata.GeoIP))
	}

	siteTmp, err := writeTemp(dstDir, sl)
	if err != nil {
		return Result{}, err
	}
	ipTmp, err := writeTemp(dstDir, il)
	if err != nil {
		os.Remove(siteTmp)
		return Result{}, err
	}
	cleanup := func() { os.Remove(siteTmp); os.Remove(ipTmp) }
	if afterTemps != nil {
		if err := afterTemps(); err != nil {
			cleanup()
			return Result{}, err
		}
	}
	if err := os.Rename(siteTmp, res.GeoSite); err != nil {
		cleanup()
		return Result{}, fmt.Errorf("merge: %w", err)
	}
	if err := os.Rename(ipTmp, res.GeoIP); err != nil {
		cleanup()
		return Result{}, fmt.Errorf("merge: %w", err)
	}
	return res, nil
}

type entry struct {
	code string
	msg  proto.Message
}

// pick returns, for each wanted tag, the entry from the first source that has
// it. A source file is read only while tags remain unresolved.
func pick(kind string, want []string, srcs []Source, names []string, from map[string]string,
	path func(Source) string, load func([]byte) ([]entry, error)) ([]proto.Message, error) {

	type need struct{ asked, key string }
	var pending []need
	seen := map[string]bool{}
	for _, t := range want {
		// "!" (Xray's reverse match) is not part of the tag.
		base, _, _ := strings.Cut(strings.TrimLeft(t, "!"), "@")
		k := strings.ToUpper(base)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		pending = append(pending, need{base, k})
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].key < pending[j].key })

	var out []proto.Message
	for _, s := range srcs {
		if len(pending) == 0 {
			break
		}
		b, err := os.ReadFile(path(s))
		if err != nil {
			return nil, fmt.Errorf("merge: read %s %s: %w", s.Name, kind, err)
		}
		es, err := load(b)
		if err != nil {
			return nil, fmt.Errorf("merge: parse %s %s: %w", s.Name, kind, err)
		}
		byKey := make(map[string]proto.Message, len(pending))
		for _, e := range es {
			k := strings.ToUpper(e.code)
			if _, ok := byKey[k]; !ok {
				byKey[k] = e.msg
			}
		}
		rest := pending[:0:0]
		for _, n := range pending {
			if m, ok := byKey[n.key]; ok {
				out = append(out, m)
				from[kind+":"+n.asked] = s.Name
			} else {
				rest = append(rest, n)
			}
		}
		pending = rest
	}
	if len(pending) > 0 {
		return nil, &MissingTagError{Tag: pending[0].asked, Kind: kind, Searched: names}
	}
	return out, nil
}

func writeTemp(dir string, m proto.Message) (string, error) {
	b, err := proto.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("merge: %w", err)
	}
	f, err := os.CreateTemp(dir, "geo-*.tmp")
	if err != nil {
		return "", fmt.Errorf("merge: %w", err)
	}
	name := f.Name()
	_, werr := f.Write(b)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(name)
		return "", fmt.Errorf("merge: %w", werr)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("merge: %w", err)
	}
	return name, nil
}
