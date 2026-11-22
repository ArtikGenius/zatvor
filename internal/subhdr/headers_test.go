package subhdr

import (
	"net/http"
	"testing"
	"time"
)

func TestUserinfo(t *testing.T) {
	h := http.Header{}
	h.Set("Subscription-Userinfo", "upload=1; download=2; total=0; expire=0")
	i := Parse(h, nil, "")
	if i.Upload != 1 || i.Download != 2 || i.Total != 0 || !i.Expire.IsZero() {
		t.Fatalf("got %+v", i)
	}
	h.Set("Subscription-Userinfo", "  upload = 5 ;download=6;;total=10 ; expire=1795000000 ; junk; x=y")
	i = Parse(h, nil, "")
	if i.Upload != 5 || i.Download != 6 || i.Total != 10 || i.Expire.Unix() != 1795000000 {
		t.Fatalf("got %+v", i)
	}
	h.Set("Subscription-Userinfo", "download=7")
	i = Parse(h, nil, "")
	if i.Upload != 0 || i.Download != 7 || i.Total != 0 || !i.Expire.IsZero() {
		t.Fatalf("missing keys: %+v", i)
	}
	h.Set("Subscription-Userinfo", "upload=abc; total=-4; expire=-1")
	i = Parse(h, nil, "")
	if i.Upload != 0 || i.Total != 0 || !i.Expire.IsZero() {
		t.Fatalf("bad values: %+v", i)
	}
}

func TestInlineFillsGapsRealWins(t *testing.T) {
	h := http.Header{}
	h.Set("Profile-Title", "Real")
	inline := map[string]string{
		"profile-title":           "base64:0JfQsNGC0LLQvtGA",
		"subscription-userinfo":   "upload=1; download=2; total=3; expire=0",
		"profile-update-interval": "6",
		"support-url":             "https://support.example.com",
	}
	i := Parse(h, inline, "")
	if i.Title != "Real" || i.Total != 3 || i.UpdateInterval != 6*time.Hour || i.SupportURL != "https://support.example.com" {
		t.Fatalf("got %+v", i)
	}
}

func TestTitle(t *testing.T) {
	h := http.Header{}
	h.Set("Profile-Title", "base64:0JfQsNGC0LLQvtGA")
	if got := Parse(h, nil, "").Title; got != "Затвор" {
		t.Fatalf("base64 title %q", got)
	}
	h.Set("Profile-Title", "Plain Title")
	if got := Parse(h, nil, "").Title; got != "Plain Title" {
		t.Fatalf("plain title %q", got)
	}
	// unpadded URL-safe variant
	h.Set("Profile-Title", "base64:UkVQTEFDRV9NRQ")
	if got := Parse(h, nil, "").Title; got != "REPLACE_ME" {
		t.Fatalf("raw base64 title %q", got)
	}
	// content-disposition fallback
	if got := Parse(http.Header{}, nil, `attachment; filename="My Sub"`).Title; got != "My Sub" {
		t.Fatalf("filename %q", got)
	}
	if got := Parse(http.Header{}, nil, `attachment; filename*=UTF-8''%D0%97%D0%B0%D1%82`).Title; got != "Зат" {
		t.Fatalf("filename* %q", got)
	}
	if got := Parse(http.Header{}, nil, "garbage;;;").Title; got != "" {
		t.Fatalf("garbage disposition %q", got)
	}
}

func TestRouting(t *testing.T) {
	link := "happ://routing/onadd/e30="
	h := http.Header{}
	h.Set("Routing", link)
	i := Parse(h, nil, "")
	if i.RoutingLink != link || !i.RoutingEnabled {
		t.Fatalf("got %+v", i)
	}
	h.Set("Routing-Enable", "false")
	if i = Parse(h, nil, ""); i.RoutingEnabled || i.RoutingLink != link {
		t.Fatalf("disabled: %+v", i)
	}
	h.Set("Routing-Enable", "0")
	if i = Parse(h, nil, ""); i.RoutingEnabled {
		t.Fatalf("0: %+v", i)
	}
	h.Set("Routing-Enable", "true")
	if i = Parse(h, nil, ""); !i.RoutingEnabled {
		t.Fatalf("true: %+v", i)
	}
	if i = Parse(http.Header{}, nil, ""); i.RoutingEnabled {
		t.Fatalf("no link: %+v", i)
	}
}

func TestHWIDResponses(t *testing.T) {
	h := http.Header{}
	h.Set("X-Hwid-Not-Supported", "true")
	i := Parse(h, nil, "")
	if !i.HWIDNotSupported || i.HWIDLimitReached {
		t.Fatalf("%+v", i)
	}
	h = http.Header{}
	h.Set("X-Hwid-Max-Devices-Reached", "true")
	if i = Parse(h, nil, ""); !i.HWIDLimitReached || i.HWIDNotSupported {
		t.Fatalf("%+v", i)
	}
	h = http.Header{}
	h.Set("X-Hwid-Limit", "true")
	if i = Parse(h, nil, ""); !i.HWIDLimitReached {
		t.Fatalf("%+v", i)
	}
	h = http.Header{}
	h.Set("X-Hwid-Active", "true")
	if i = Parse(h, nil, ""); i.HWIDLimitReached || i.HWIDNotSupported {
		t.Fatalf("active must set nothing: %+v", i)
	}
}

func TestHappLike(t *testing.T) {
	for _, k := range []string{"Routing", "Announce", "Hide-Settings", "Sub-Info-Text"} {
		h := http.Header{}
		h.Set(k, "x")
		if !Parse(h, nil, "").HappLike {
			t.Errorf("%s should be happ-like", k)
		}
	}
	if !Parse(http.Header{}, map[string]string{"sub-info-color": "red"}, "").HappLike {
		t.Error("inline sub-info-* should be happ-like")
	}
	h := http.Header{}
	h.Set("Profile-Title", "x")
	h.Set("Support-Url", "https://example.com")
	if Parse(h, nil, "").HappLike {
		t.Error("plain headers are not happ-like")
	}
}

func TestInfoFields(t *testing.T) {
	h := http.Header{}
	h.Set("Profile-Update-Interval", "12")
	h.Set("Profile-Web-Page-Url", "https://example.com/p")
	h.Set("Announce", "base64:0JfQsNGC0LLQvtGA")
	i := Parse(h, nil, "")
	if i.UpdateInterval != 12*time.Hour || i.WebPageURL != "https://example.com/p" || i.Announce != "Затвор" {
		t.Fatalf("%+v", i)
	}
	h.Set("Profile-Update-Interval", "bad")
	if Parse(h, nil, "").UpdateInterval != 0 {
		t.Fatal("bad interval")
	}
}
