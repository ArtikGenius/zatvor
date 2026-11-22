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
