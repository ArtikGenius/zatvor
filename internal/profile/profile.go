// Package profile holds the user's subscriptions and the profiles (proxy
// servers) inside them, and merges a freshly fetched subscription into the
// stored one without losing the user's selection.
package profile

import (
	"net/url"
	"strings"
	"time"

	"github.com/ArtikGenius/zatvor/internal/happ"
	"github.com/ArtikGenius/zatvor/internal/link"
	"github.com/ArtikGenius/zatvor/internal/ojson"
	"github.com/ArtikGenius/zatvor/internal/subhdr"
)

// SourceKind says where a profile's routing rules come from.
type SourceKind string

const (
	// SourceProvider uses the subscription's routing header (links only).
	SourceProvider SourceKind = "provider"
	// SourcePreset uses one of Zatvor's built-in presets.
	SourcePreset SourceKind = "preset"
	// SourceHapp uses a Happ routing profile the user imported.
	SourceHapp SourceKind = "happ"
)

// Preset is a built-in routing preset.
type Preset string

const (
	PresetAllProxy     Preset = "all-proxy"
	PresetAllExceptRU  Preset = "all-except-ru"
	PresetSelectedOnly Preset = "selected-only"
)

// Source is a profile's routing source. Happ is set for SourceHapp and, for
// SourceProvider, points at the subscription's ProviderRouting.
type Source struct {
	Kind   SourceKind
	Preset Preset
	Happ   *happ.Profile
}

// Profile is one proxy server: a share-link Node or a provider JSON Config.
// Exactly one of Node and Config is set.
type Profile struct {
	ID             string
	Name           string
	SubscriptionID string
	Node           *link.Node
	Config         *ojson.Object
	RoutingSource  Source
	Disabled       bool
}

// Subscription is a provider URL and the profiles last fetched from it.
// URL is a secret: it is stored only in the encrypted store and never put in
// errors or logs.
type Subscription struct {
	ID, URL, Title, UA string
	SendDevice         bool
	Info               subhdr.Info
	ProviderRouting    *happ.Profile
	LastGood           time.Time
	Profiles           []Profile
}

// URLHost reduces a subscription URL (or a URL already reduced to its host)
// to the host and port: no user info, path, query or fragment. The API and
// the diagnostics list subscriptions by it.
func URLHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	if i := strings.IndexAny(raw, "/?#"); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		raw = raw[i+1:]
	}
	return raw
}
