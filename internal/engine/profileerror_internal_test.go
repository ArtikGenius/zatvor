package engine

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/ArtikGenius/zatvor/internal/happ"
	"github.com/ArtikGenius/zatvor/internal/link"
	"github.com/ArtikGenius/zatvor/internal/profile"
)

// TestProfileErrorScrubs: a cause quoting a profile secret or its geo URL
// is scrubbed before it reaches Error.Message.
func TestProfileErrorScrubs(t *testing.T) {
	const secret = "REPLACE_ME_secret_pw"
	p := profile.Profile{ID: "p", Node: &link.Node{Scheme: "trojan", Address: "h.example", Port: 443, Params: map[string]string{"password": secret}},
		RoutingSource: profile.Source{Kind: profile.SourceHapp, Happ: &happ.Profile{Geositeurl: "https://geo.example/s.dat"}}}
	e := profileError(CodeBuildFailed, "cannot build", errors.New("bad password "+secret), p)
	if strings.Contains(e.Message, secret) || e.Code != CodeBuildFailed {
		t.Fatalf("message = %q", e.Message)
	}
	e = profileError(WarnGeoFetchFailed, "fetch", &url.Error{Op: "Get", URL: "https://geo.example/s.dat", Err: errors.New("refused")}, p)
	if strings.Contains(e.Message, "geo.example") || !strings.Contains(e.Message, "refused") {
		t.Fatalf("message = %q", e.Message)
	}
	e = profileError(WarnGeoFetchFailed, "fetch", errors.New("checksum of https://geo.example/s.dat differs"), p)
	if strings.Contains(e.Message, "geo.example") {
		t.Fatalf("message = %q", e.Message)
	}
}
