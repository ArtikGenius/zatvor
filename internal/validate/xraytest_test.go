package validate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain turns the test binary into a fake "xray": started as
// `<bin> run -test -config stdin:` it rejects the config, echoing it.
func TestMain(m *testing.M) {
	if len(os.Args) == 5 && os.Args[1] == "run" && os.Args[2] == "-test" {
		in, _ := io.ReadAll(os.Stdin)
		os.Stderr.WriteString("Failed to start: " + string(in) + "\n")
		os.Exit(23)
	}
	os.Exit(m.Run())
}

func TestXrayTestErrorsTyped(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := []byte(`{"outbounds":[{"protocol":"vless","settings":{"password":"SECRET_REPLACE_ME"}}]}`)
	err = XrayTest(context.Background(), self, cfg, t.TempDir())
	var xe *XrayTestError
	if !errors.As(err, &xe) {
		t.Fatalf("err = %#v, want *XrayTestError", err)
	}
	if xe.ExitCode != 23 || !strings.Contains(xe.Output, "Failed to start") || strings.Contains(err.Error(), "SECRET_REPLACE_ME") {
		t.Fatalf("XrayTestError = %+v", xe)
	}
	missing := filepath.Join(t.TempDir(), "no-xray")
	if err := XrayTest(context.Background(), missing, cfg, t.TempDir()); !errors.Is(err, ErrXrayNotRun) {
		t.Fatalf("missing binary: %v, want ErrXrayNotRun", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := XrayTest(ctx, self, cfg, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v, want context.Canceled", err)
	}
}

func TestScrubSecrets(t *testing.T) {
	cfg := []byte(`{
	  "outbounds":[
	    {"protocol":"vless","settings":{"vnext":[{"address":"example.com","port":443,"users":[{"id":"11111111-2222-3333-4444-555555555555","encryption":"none"}]}]},
	     "streamSettings":{"security":"reality","realitySettings":{"publicKey":"PUBKEY_REPLACE_ME","shortId":"abcd1234","privateKey":"PRIVKEY_REPLACE_ME"}}},
	    {"protocol":"hysteria","streamSettings":{"hysteriaSettings":{"auth":"AUTHSECRET_REPLACE"}}},
	    {"protocol":"shadowsocks","settings":{"servers":[{"password":"PASSWORD_REPLACE_ME","pass":"PASSX_REPLACE"}]}},
	    {"protocol":"wireguard","settings":{"secretKey":"SK_REPLACE_ME","peers":[{"preSharedKey":"PSK_REPLACE_ME"}]}}
	  ],
	  "inbounds":[{"protocol":"socks","settings":{"accounts":[{"user":"alice_user","pass":"inpass_REPLACE"}]}}]
	}`)
	msg := "Xray 1.x: failed to build: bad id 11111111-2222-3333-4444-555555555555 key PUBKEY_REPLACE_ME pk PRIVKEY_REPLACE_ME sid abcd1234 " +
		"auth AUTHSECRET_REPLACE pw PASSWORD_REPLACE_ME PASSX_REPLACE sk SK_REPLACE_ME psk PSK_REPLACE_ME user alice_user inpass_REPLACE; port 443 ok"
	got := scrub(msg, cfg)
	for _, s := range []string{"11111111-2222", "PUBKEY_", "PRIVKEY_", "abcd1234", "AUTHSECRET", "PASSWORD_", "PASSX_", "SK_REPLACE", "PSK_", "alice_user", "inpass_"} {
		if strings.Contains(got, s) {
			t.Errorf("secret %q leaked: %s", s, got)
		}
	}
	if !strings.Contains(got, "failed to build") || !strings.Contains(got, "port 443 ok") || !strings.Contains(got, "***") {
		t.Errorf("message mangled: %s", got)
	}
}

func TestScrubKeepsMessageOnBadConfig(t *testing.T) {
	if got := scrub("some error", []byte("{")); got != "some error" {
		t.Fatalf("got %q", got)
	}
}

func TestScrubVLESSEncryption(t *testing.T) {
	enc := "mlkem768x25519plus.native.0rtt.KEYMATERIAL_REPLACE_ME"
	cfg := []byte(`{"outbounds":[{"protocol":"vless","tag":"proxy","settings":{"vnext":[{"address":"host.example.com","users":[{"encryption":"` + enc + `"}]}]},
	 "streamSettings":{"network":"kcp","security":"tls","tlsSettings":{"serverName":"sni.example.com"},"kcpSettings":{"seed":"SEEDVALUE_REPLACE"},
	 "finalmask":{"udp":[{"type":"salamander","settings":{"password":"OBFSPW_REPLACE_ME","token":"TOKEN_REPLACE_ME"}}]}}}]}`)
	msg := "bad " + enc + " at host.example.com sni.example.com seed SEEDVALUE_REPLACE obfs OBFSPW_REPLACE_ME tok TOKEN_REPLACE_ME"
	got := scrub(msg, cfg)
	for _, s := range []string{"KEYMATERIAL", "mlkem768", "host.example", "sni.example", "SEEDVALUE", "OBFSPW", "TOKEN_"} {
		if strings.Contains(got, s) {
			t.Errorf("%q leaked: %s", s, got)
		}
	}
}

func TestScrubUUIDVariants(t *testing.T) {
	id := "AbCdEf01-2345-6789-abcd-ef0123456789"
	cfg := []byte(`{"outbounds":[{"protocol":"vless","settings":{"vnext":[{"users":[{"id":"` + id + `"}]}]}}]}`)
	msg := id + " " + strings.ToLower(id) + " " + strings.ToUpper(id) + " abcdef0123456789abcdef0123456789 ABCDEF0123456789ABCDEF0123456789 " +
		`quoted \"` + id + `\"`
	got := scrub(msg, cfg)
	for _, s := range []string{"2345-6789", "23456789", "ABCDEF01", "abcdef01"} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(s)) {
			t.Errorf("%q leaked: %s", s, got)
		}
	}
	esc := scrub(`json "a\u003cb\u003c\u003c\u003c\u003c"`, []byte(`{"x":{"password":"a<b<<<<"}}`))
	if strings.Contains(esc, "u003c") {
		t.Errorf("escaped form leaked: %s", esc)
	}
}

func TestScrubKeepsNonSecret(t *testing.T) {
	cfg := []byte(`{"log":{"loglevel":"warning"},"outbounds":[{"protocol":"nosuchproto","tag":"proxy-out","settings":{"password":"SECRET_REPLACE_ME"},
	 "streamSettings":{"network":"tcp","security":"reality","realitySettings":{"fingerprint":"chrome"}}}],"routing":{"rules":[{"outboundTag":"proxy-out"}]}}`)
	msg := "unknown protocol nosuchproto tag proxy-out network tcp security reality fingerprint chrome level warning outbound proxy-out SECRET_REPLACE_ME"
	got := scrub(msg, cfg)
	if strings.Contains(got, "SECRET_REPLACE_ME") {
		t.Errorf("secret leaked: %s", got)
	}
	for _, s := range []string{"nosuchproto", "proxy-out", "tcp", "reality", "chrome", "warning"} {
		if !strings.Contains(got, s) {
			t.Errorf("%q was scrubbed: %s", s, got)
		}
	}
}

func TestSecretVariantsCoverURLForms(t *testing.T) {
	s := `tok en/+&=?"\#%😀_REPLACE`
	got := map[string]bool{}
	for _, v := range SecretVariants(s) {
		got[v] = true
	}
	j, _ := json.Marshal(s)
	for _, want := range []string{s, url.QueryEscape(s), url.PathEscape(s), string(j[1 : len(j)-1]), strings.ToUpper(s)} {
		if !got[want] {
			t.Errorf("SecretVariants misses %q", want)
		}
	}
}

func TestScrubAll(t *testing.T) {
	pw := `pw"\#%😀_REPLACE_ME`
	j, _ := json.Marshal(pw)
	msg := "a " + pw + " b " + url.QueryEscape(pw) + " c " + string(j) + " d short ok"
	got := ScrubAll(msg, []string{pw, "short", ""})
	for _, leak := range []string{pw, url.QueryEscape(pw), string(j[1 : len(j)-1])} {
		if strings.Contains(got, leak) {
			t.Errorf("%q leaked: %s", leak, got)
		}
	}
	// Values shorter than the secret minimum are left alone.
	if !strings.Contains(got, "short ok") || !strings.HasPrefix(got, "a *** b *** c ") {
		t.Errorf("message mangled: %s", got)
	}
}

func TestSecretValuesSkipsPlainKeys(t *testing.T) {
	vals := SecretValues([]byte(`{"type":"reality-x","security":"reality","id":"SECRET_ID_REPLACE","nested":[{"pass":"PASS_REPLACE"}]}`))
	got := map[string]bool{}
	for _, v := range vals {
		got[v] = true
	}
	if !got["SECRET_ID_REPLACE"] || !got["PASS_REPLACE"] || got["reality"] || got["reality-x"] {
		t.Errorf("SecretValues = %q", vals)
	}
	if SecretValues([]byte("{")) != nil {
		t.Error("invalid JSON gave values")
	}
}
