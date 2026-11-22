package profile

import (
	"encoding/base64"
	"reflect"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/link"
	"github.com/ArtikGenius/zatvor/internal/ojson"
	"github.com/ArtikGenius/zatvor/internal/subbody"
	"github.com/ArtikGenius/zatvor/internal/subfetch"
	"github.com/ArtikGenius/zatvor/internal/subhdr"
)

var t0 = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

func node(host string, port int, remark string) link.Node {
	return link.Node{
		Scheme: "vless", Remark: remark, Address: host, Port: port,
		Params: map[string]string{"id": "11111111-2222-3333-4444-555555555555", "type": "tcp", "security": "reality", "sni": "example.com"},
	}
}

func linksResult(nodes ...link.Node) subfetch.Result {
	return subfetch.Result{
		Body:   subbody.Result{Kind: subbody.Links, Nodes: nodes},
		Status: 200,
	}
}

func baseSub() Subscription {
	return Subscription{ID: "sub1", URL: "https://sub.example.com/REPLACE_ME", Title: "T", UA: "Zatvor/test (linux)"}
}

func mustConfig(t *testing.T, s string) *ojson.Object {
	t.Helper()
	v, err := ojson.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v.(*ojson.Object)
}

func ids(ps []Profile) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func TestSelectionSurvivesUpdate(t *testing.T) {
	a, b := node("a.example.com", 443, "A"), node("b.example.com", 443, "B")
	first, _, err := Apply(baseSub(), linksResult(a, b), t0)
	if err != nil {
		t.Fatal(err)
	}
	selected := first.Profiles[0].ID
	if selected != link.ID(a) {
		t.Fatalf("profile ID = %s, want link.ID", selected)
	}
	first.Profiles[0].Disabled = true
	first.Profiles[0].RoutingSource = Source{Kind: SourcePreset, Preset: PresetSelectedOnly}

	a2 := a
	a2.Remark = "A renamed"
	second, diff, err := Apply(first, linksResult(b, a2), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var got *Profile
	for i := range second.Profiles {
		if second.Profiles[i].ID == selected {
			got = &second.Profiles[i]
		}
	}
	if got == nil {
		t.Fatal("selected ID vanished after reorder and remark change")
	}
	if got.Name != "A renamed" || !got.Disabled || got.RoutingSource.Preset != PresetSelectedOnly {
		t.Fatalf("profile = %+v: want new name, kept user settings", got)
	}
	if !reflect.DeepEqual(diff.Changed, []string{selected}) || len(diff.Added)+len(diff.Removed) != 0 {
		t.Fatalf("diff = %+v", diff)
	}
	if !second.LastGood.Equal(t0.Add(time.Hour)) {
		t.Fatal("LastGood not advanced")
	}
}

func TestBadUpdateKeepsOld(t *testing.T) {
	old, _, err := Apply(baseSub(), linksResult(node("a.example.com", 443, "A")), t0)
	if err != nil {
		t.Fatal(err)
	}
	oldBytes := mustMarshal(t, old)
	cases := map[string]subfetch.Result{
		"empty body":   {Problem: subfetch.EmptyBody},
		"device limit": {Problem: subfetch.DeviceLimit, Info: subhdr.Info{HWIDLimitReached: true}},
		"only skipped": {Body: subbody.Result{Kind: subbody.Links, Skipped: []subbody.Skip{{Line: 1, Scheme: "vless", Reason: "bad"}}}, Status: 200},
		"problem wins over nodes": {
			Body:    subbody.Result{Kind: subbody.Links, Nodes: []link.Node{node("z.example.com", 1, "Z")}},
			Problem: subfetch.HTTPStatus,
		},
	}
	for name, res := range cases {
		got, diff, err := Apply(old, res, t0.Add(time.Hour))
		if err == nil {
			t.Errorf("%s: want error", name)
		}
		if !reflect.DeepEqual(got, old) || string(mustMarshal(t, got)) != string(oldBytes) {
			t.Errorf("%s: subscription changed", name)
		}
		if len(diff.Added)+len(diff.Removed)+len(diff.Changed) != 0 {
			t.Errorf("%s: diff = %+v", name, diff)
		}
	}
}

func TestDiff(t *testing.T) {
	keep := node("keep.example.com", 443, "keep")
	gone := node("gone.example.com", 443, "gone")
	chg := node("chg.example.com", 443, "chg")
	portMove := node("move.example.com", 443, "move")
	old, _, err := Apply(baseSub(), linksResult(keep, gone, chg, portMove), t0)
	if err != nil {
		t.Fatal(err)
	}
	chg2 := chg
	chg2.Params = map[string]string{}
	for k, v := range chg.Params {
		chg2.Params[k] = v
	}
	chg2.Params["sni"] = "other.example.com" // not part of the ID
	added := node("new.example.com", 443, "new")
	moved := portMove
	moved.Port = 8443 // port is part of the ID: Removed + Added
	_, diff, err := Apply(old, linksResult(keep, chg2, added, moved), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := Diff{
		Added:   []string{link.ID(added), link.ID(moved)},
		Removed: []string{link.ID(gone), link.ID(portMove)},
		Changed: []string{link.ID(chg)},
	}
	if !reflect.DeepEqual(diff, want) {
		t.Fatalf("diff = %+v\nwant   %+v", diff, want)
	}
}

func TestDiffJSONConfigChange(t *testing.T) {
	cfg := func(sni string) *ojson.Object {
		return mustConfig(t, `{"remarks":"X","outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"a.example.com","port":443,"users":[{"id":"u1"}]}]},"streamSettings":{"network":"tcp","realitySettings":{"serverName":"`+sni+`"}}}]}`)
	}
	res := func(c *ojson.Object) subfetch.Result {
		return subfetch.Result{Body: subbody.Result{Kind: subbody.XrayJSON, Configs: []*ojson.Object{c}}, Status: 200}
	}
	old, _, err := Apply(baseSub(), res(cfg("one.example.com")), t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(old.Profiles) != 1 || old.Profiles[0].Config == nil || old.Profiles[0].Name != "X" {
		t.Fatalf("profiles = %+v", old.Profiles)
	}
	_, same, _ := Apply(old, res(cfg("one.example.com")), t0)
	if len(same.Changed) != 0 {
		t.Fatalf("identical config reported changed: %+v", same)
	}
	_, diff, err := Apply(old, res(cfg("two.example.com")), t0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(diff.Changed, []string{old.Profiles[0].ID}) {
		t.Fatalf("diff = %+v", diff)
	}
}

func TestConfigIDUsesFirstProxyOutbound(t *testing.T) {
	a := mustConfig(t, `{"outbounds":[{"protocol":"freedom","tag":"direct"},{"protocol":"vless","settings":{"vnext":[{"address":"A.example.com","port":443,"users":[{"id":"u1"}]}]},"streamSettings":{"network":"ws"}},{"protocol":"trojan","settings":{"servers":[{"address":"z.example.com","port":1,"password":"p"}]}}]}`)
	b := mustConfig(t, `{"remarks":"different","routing":{},"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"a.example.com","port":443,"users":[{"id":"u1"}]}]},"streamSettings":{"network":"ws"}}]}`)
	ia, ok := ConfigID(a)
	ib, ok2 := ConfigID(b)
	if !ok || !ok2 || ia != ib || len(ia) != 16 {
		t.Fatalf("ids %q %q", ia, ib)
	}
	c := mustConfig(t, `{"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"a.example.com","port":444,"users":[{"id":"u1"}]}]},"streamSettings":{"network":"ws"}}]}`)
	if ic, _ := ConfigID(c); ic == ia {
		t.Fatal("port must change the ID")
	}
	if _, ok := ConfigID(mustConfig(t, `{"outbounds":[{"protocol":"freedom"}]}`)); ok {
		t.Fatal("config without a proxy outbound has no ID")
	}
}

func routingLink() string {
	return "happ://routing/onadd/" + base64.StdEncoding.EncodeToString([]byte(`{"Name":"Prov","GlobalProxy":"true","DirectSites":["geosite:ru"]}`))
}

func TestRoutingHeaderApplied(t *testing.T) {
	res := linksResult(node("a.example.com", 443, "A"), node("b.example.com", 443, "B"))
	res.Info = subhdr.Info{RoutingLink: routingLink(), RoutingEnabled: true}
	old := baseSub()
	got, _, err := Apply(old, res, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderRouting == nil || got.ProviderRouting.Name != "Prov" {
		t.Fatalf("ProviderRouting = %+v", got.ProviderRouting)
	}
	for _, p := range got.Profiles {
		if p.RoutingSource.Kind != SourceProvider || p.RoutingSource.Happ != got.ProviderRouting {
			t.Fatalf("profile %s source = %+v", p.ID, p.RoutingSource)
		}
	}

	// disabled routing header: not applied
	res.Info.RoutingEnabled = false
	off, _, _ := Apply(old, res, t0)
	if off.ProviderRouting != nil {
		t.Fatal("disabled header applied")
	}

	// JSON result: header ignored
	jres := subfetch.Result{
		Body:   subbody.Result{Kind: subbody.XrayJSON, Configs: []*ojson.Object{mustConfig(t, `{"outbounds":[{"protocol":"vless","settings":{"address":"a.example.com","port":443,"id":"u"}}]}`)}},
		Info:   subhdr.Info{RoutingLink: routingLink(), RoutingEnabled: true},
		Status: 200,
	}
	j, _, err := Apply(old, jres, t0)
	if err != nil {
		t.Fatal(err)
	}
	if j.ProviderRouting != nil {
		t.Fatal("routing header must be ignored for JSON subscriptions")
	}
	if j.Profiles[0].RoutingSource.Kind == SourceProvider {
		t.Fatal("JSON profile must not use provider routing")
	}
}

func TestRoutingHeaderDecodeErrorKeepsOld(t *testing.T) {
	res := linksResult(node("a.example.com", 443, "A"))
	res.Info = subhdr.Info{RoutingLink: routingLink(), RoutingEnabled: true}
	good, _, err := Apply(baseSub(), res, t0)
	if err != nil || good.ProviderRouting == nil {
		t.Fatal("setup failed")
	}
	bad := res
	bad.Info = subhdr.Info{RoutingLink: "happ://routing/onadd/!!!notbase64", RoutingEnabled: true}
	got, _, err := Apply(good, bad, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderRouting == nil || got.ProviderRouting.Name != "Prov" {
		t.Fatalf("undecodable header wiped routing: %+v", got.ProviderRouting)
	}
	absent := res
	absent.Info = subhdr.Info{}
	cleared, _, _ := Apply(good, absent, t0)
	if cleared.ProviderRouting != nil {
		t.Fatal("absent header must clear")
	}
}
