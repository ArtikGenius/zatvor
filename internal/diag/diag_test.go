package diag_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/diag"
	"github.com/ArtikGenius/zatvor/internal/engine"
	"github.com/ArtikGenius/zatvor/internal/link"
	"github.com/ArtikGenius/zatvor/internal/ojson"
	"github.com/ArtikGenius/zatvor/internal/profile"
	"github.com/ArtikGenius/zatvor/internal/settings"
	"github.com/ArtikGenius/zatvor/internal/validate"
)

// Awkward secret-looking test values. None is real.
const (
	pathTok  = "Pa th+/%😀_PATH_REPLACE"
	queryTok = `q&t=k+e y/%"#_QUERY_REPLACE`
	nodeUUID = "00000000-0000-4000-8000-0000000c0de1"
	nodePass = `pw"\#%😀_REPLACE_ME`
	pbk      = "PBK_REPLACE_ME_0000000000000000000000000"
	sid      = "sid_REPLACE_1234"
	sni      = "sni-secret.example.net"
	jsonPass = `json"pass\#%😀_REPLACE`
	server   = "node-secret.example.net"
	subHost  = "sub.example.org"
	profID   = "prof-0123456789ab"
)

var subURL = "https://" + subHost + "/api/s/" + url.PathEscape(pathTok) + "?token=" + url.QueryEscape(queryTok) + "&n=1"

// forms are the ways s may be written somewhere: raw, URL query- and
// path-escaped, JSON-escaped, Go-quoted, and every variant validate.Scrub
// covers.
func forms(s string) []string {
	j, _ := json.Marshal(s)
	q := strconv.Quote(s)
	out := []string{s, url.QueryEscape(s), url.PathEscape(s), string(j[1 : len(j)-1]), q[1 : len(q)-1]}
	return append(out, validate.SecretVariants(s)...)
}

func secretSubs(t *testing.T) []profile.Subscription {
	t.Helper()
	node := &link.Node{Scheme: "vless", Remark: "alpha", Address: server, Port: 443,
		Params: map[string]string{"id": nodeUUID, "security": "reality", "pbk": pbk, "sid": sid, "sni": sni, "type": "tcp"},
		Raw:    "vless://" + nodeUUID + "@" + server + ":443?security=reality&pbk=" + pbk + "&sid=" + sid + "&sni=" + sni + "#alpha"}
	trojan := &link.Node{Scheme: "trojan", Address: server, Port: 8443, Params: map[string]string{"password": nodePass},
		Raw: "trojan://" + url.PathEscape(nodePass) + "@" + server + ":8443"}
	cfgRaw, _ := json.Marshal(map[string]any{"outbounds": []any{map[string]any{"protocol": "shadowsocks",
		"settings": map[string]any{"servers": []any{map[string]any{"address": server, "password": jsonPass}}}}}})
	cfg, err := ojson.Parse(cfgRaw)
	if err != nil {
		t.Fatal(err)
	}
	obj, ok := cfg.(*ojson.Object)
	if !ok {
		t.Fatalf("config parsed as %T", cfg)
	}
	return []profile.Subscription{{
		ID: "sub-1", URL: subURL, Title: "Provider", SendDevice: true,
		Profiles: []profile.Profile{
			{ID: profID, Name: "alpha", SubscriptionID: "sub-1", Node: node},
			{ID: "prof-trojan00000", Name: "beta", SubscriptionID: "sub-1", Node: trojan},
			{ID: "prof-json0000000", Name: "gamma", SubscriptionID: "sub-1", Config: obj},
		},
	}}
}

func secrets() []string {
	return []string{subURL, pathTok, queryTok, nodeUUID, nodePass, pbk, sid, sni, jsonPass, server, profID,
		"/api/s/" + url.PathEscape(pathTok), "token=" + url.QueryEscape(queryTok)}
}

// leakySources puts every form of every secret into every source.
func leakySources(t *testing.T) diag.Sources {
	t.Helper()
	var all []string
	for _, s := range secrets() {
		all = append(all, forms(s)...)
	}
	text := "kept line\n" + strings.Join(all, "\n") + "\nkept line\n"
	prev, _ := json.Marshal(map[string]any{"server": nodePass, "url": subURL})
	journal := `{"id":"j1","kind":"sysproxy","target":"lan","prev":` + string(prev) + `,"applied":{},"at":"2026-11-22T10:00:00Z"}` + "\n"
	st := engine.State{Phase: engine.Failed, Profile: profID,
		Err:      &engine.Error{Code: "xray_exited", Message: "Xray exited: " + nodePass + " " + subURL},
		Warnings: []engine.Error{{Code: "w", Message: strings.Join(all, " ")}},
		Check:    engine.CheckResult{Status: engine.Unknown}}
	set := settings.Defaults()
	set.ActiveProfile = profID
	set.LogLevel = nodeUUID
	return diag.Sources{
		Versions:      diag.Versions{Zatvord: "0.0.0-test", Xray: "v26.11.9", OS: "testos", Arch: "testarch", Go: "go-test"},
		Settings:      set,
		Journal:       []byte(journal + "{\"done\":\"" + queryTok + "\"}\n"),
		DaemonLog:     []byte(text),
		XrayOutput:    text,
		State:         st,
		Subscriptions: secretSubs(t),
		Now:           time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
	}
}

func unzip(t *testing.T, b []byte) ([]string, map[string][]byte) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, f.Name)
		files[f.Name] = body
	}
	return names, files
}

func bundle(t *testing.T, src diag.Sources) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := diag.Bundle(&buf, src); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDiagRedactsEverySecret(t *testing.T) {
	raw := bundle(t, leakySources(t))
	_, files := unzip(t, raw)
	if len(files) == 0 {
		t.Fatal("empty bundle")
	}
	var all []byte
	for name, body := range files {
		all = append(all, body...)
		for _, s := range secrets() {
			for _, f := range forms(s) {
				if len(f) > 0 && bytes.Contains(body, []byte(f)) {
					t.Errorf("%s holds secret %q in the form %q", name, s, f)
				}
			}
		}
	}
	// Everything that is not a secret is still there.
	for name, want := range map[string]string{
		"zatvord.log": "kept line", "xray.log": "kept line", "subscriptions.json": subHost,
		"state.json": `"phase": "failed"`, "versions.json": "0.0.0-test", "journal.jsonl": `"kind":"sysproxy"`,
	} {
		if !bytes.Contains(files[name], []byte(want)) {
			t.Errorf("%s lost %q:\n%s", name, want, files[name])
		}
	}
	if !bytes.Contains(all, []byte("***")) {
		t.Error("nothing was redacted")
	}
}

func TestDiagNoFilesOutsideList(t *testing.T) {
	want := []string{"versions.json", "settings.json", "journal.jsonl", "zatvord.log", "xray.log", "state.json", "subscriptions.json"}
	if fmt.Sprint(diag.Files) != fmt.Sprint(want) {
		t.Fatalf("diag.Files = %q, want %q", diag.Files, want)
	}
	for _, src := range []diag.Sources{{}, leakySources(t)} {
		names, files := unzip(t, bundle(t, src))
		if fmt.Sprint(names) != fmt.Sprint(want) {
			t.Errorf("bundle holds %q, want exactly %q", names, want)
		}
		for _, n := range names {
			if strings.Contains(n, "token") || strings.Contains(n, "api.json") {
				t.Errorf("bundle holds %q", n)
			}
		}
		var subs struct {
			Subscriptions []map[string]any `json:"subscriptions"`
		}
		if err := json.Unmarshal(files["subscriptions.json"], &subs); err != nil {
			t.Fatalf("subscriptions.json: %v", err)
		}
		for _, s := range subs.Subscriptions {
			if len(s) != 1 || s["host"] != subHost {
				t.Errorf("subscription entry %v, want only the host", s)
			}
		}
	}
}

func TestDiagLogTail(t *testing.T) {
	var log strings.Builder
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(&log, "line %03d\n", i)
	}
	_, files := unzip(t, bundle(t, diag.Sources{DaemonLog: []byte(log.String())}))
	got := strings.Split(strings.TrimSuffix(string(files["zatvord.log"]), "\n"), "\n")
	if len(got) != diag.LogLines || got[0] != "line 051" || got[len(got)-1] != "line 250" {
		t.Errorf("log tail has %d lines, %q .. %q; want %d lines 051..250", len(got), got[0], got[len(got)-1], diag.LogLines)
	}
}
