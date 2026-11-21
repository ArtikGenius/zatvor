package main

import "testing"

// Test inputs are assembled from pieces so that this file itself passes the
// scan it tests.

func rules(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	return out
}

func wantRules(t *testing.T, got []Finding, want ...string) {
	t.Helper()
	g := rules(got)
	if len(g) != len(want) {
		t.Fatalf("findings = %v, want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("findings = %v, want %v", g, want)
		}
	}
}

func TestScanFlagsSubscriptionURL(t *testing.T) {
	real := []byte("https://panel.real-" + "host.ru:2096/sub/abc123")
	got := Scan("notes.md", real, nil)
	wantRules(t, got, "subscription-url")
	if got[0].Path != "notes.md" || got[0].Line != 1 {
		t.Fatalf("finding = %+v", got[0])
	}

	for _, ok := range []string{
		"https://sub.example.net/sub/k2f9",
		"https://sub.example.com/sub/k2f9",
		"https://sub.example.org/sub/k2f9",
	} {
		wantRules(t, Scan("notes.md", []byte(ok), nil))
	}
}

func TestScanFlagsUnknownUUID(t *testing.T) {
	id := "3f1c9a52-" + "7b1e-4d3a-9c55-" + "0a1b2c3d4e5f"
	content := []byte("vless://" + id + "@1.2.3.4:443")
	wantRules(t, Scan("a.txt", content, nil), "uuid")
	wantRules(t, Scan("a.txt", content, AllowList{id: {}}))
}

func TestScanFlagsPublicIPv4InFixtures(t *testing.T) {
	for _, ip := range []string{"8.8.8.8", "93.184.216.34"} {
		wantRules(t, Scan("internal/x/testdata/a.json", []byte(`{"ip":"`+ip+`"}`), nil), "ipv4")
	}
	wantRules(t, Scan("internal/x/testdata/a.json", []byte("8.8.8.8"), AllowList{"8.8.8.8": {}}))
	for _, ip := range []string{
		"192.0.2.7", "198.51.100.9", "203.0.113.200",
		"10.1.2.3", "172.16.0.1", "192.168.1.1", "127.0.0.1", "0.0.0.0",
	} {
		wantRules(t, Scan("testdata/a.json", []byte(ip), nil))
	}
	// the rule applies to testdata only
	wantRules(t, Scan("docs/a.md", []byte("8.8.8.8"), nil))
}

func TestScanFlagsPrivateKeys(t *testing.T) {
	key := "uJ3kQ0Zr5y1XbL8mP2nV7sT4wA9cE6hG1dF0iK3oM5q" + "="
	if len(key) != 44 {
		t.Fatalf("test key length %d", len(key))
	}
	wantRules(t, Scan("c.json", []byte(`"privateKey": "`+key+`"`), nil), "private-key")
	pem := "-----BEGIN " + "OPENSSH PRIVATE KEY-----"
	wantRules(t, Scan("id", []byte(pem), nil), "private-key")
	wantRules(t, Scan("id", []byte("-----BEGIN "+"PRIVATE KEY-----"), nil), "private-key")
	wantRules(t, Scan("c.json", []byte(`"privateKey": "`+key+`"`), AllowList{key: {}}))
}

func TestScanFlagsINIKeys(t *testing.T) {
	key := "uJ3kQ0Zr5y1XbL8mP2nV7sT4wA9cE6hG1dF0iK3oM5q" + "="
	for _, name := range []string{"PrivateKey", "presharedkey", "HEADERPROTECTIONKEY"} {
		line := []byte(name + " = " + key + "\r\n")
		wantRules(t, Scan("a.conf", line, nil), "ini-key")
		wantRules(t, Scan("a.conf", line, AllowList{key: {}}))
	}
	// 43-char value without padding is also flagged.
	wantRules(t, Scan("a.conf", []byte("PrivateKey = "+key[:43]), nil), "ini-key")
	// PublicKey is not secret.
	wantRules(t, Scan("a.conf", []byte("PublicKey = "+key), nil))
}

func TestScanReportsLineNumbers(t *testing.T) {
	got := Scan("x.md", []byte("ok\nok\nhttps://panel.real-"+"host.ru/sub/tok\n"), nil)
	if len(got) != 1 || got[0].Line != 3 {
		t.Fatalf("got %+v", got)
	}
}

func TestParseAllowList(t *testing.T) {
	a := ParseAllowList([]byte("# comment\n\n8.8.8.8\n  uuid-1  # trailing\n"))
	if _, ok := a["8.8.8.8"]; !ok {
		t.Fatal("missing literal")
	}
	if _, ok := a["uuid-1"]; !ok {
		t.Fatal("missing trimmed literal")
	}
	if len(a) != 2 {
		t.Fatalf("len = %d", len(a))
	}
}
