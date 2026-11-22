package validate

import (
	"strings"
	"testing"
)

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
