package link

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ArtikGenius/zatvor/internal/ojson"
	"github.com/xtls/xray-core/infra/conf"
)

type corpusCase struct {
	file string
	line int
	link string
	want Node
}

func loadCorpus(t *testing.T) []corpusCase {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "links", "*.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpus files: %v", err)
	}
	var cases []corpusCase
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		n := 0
		for sc.Scan() {
			n++
			text := sc.Text()
			if strings.TrimSpace(text) == "" || strings.HasPrefix(text, "#") {
				continue
			}
			l, exp, ok := strings.Cut(text, "\t")
			if !ok {
				t.Fatalf("%s:%d: no tab separator", f, n)
			}
			var want Node
			if err := json.Unmarshal([]byte(exp), &want); err != nil {
				t.Fatalf("%s:%d: bad expected node: %v", f, n, err)
			}
			if want.Params == nil {
				want.Params = map[string]string{}
			}
			cases = append(cases, corpusCase{filepath.Base(f), n, l, want})
		}
		fh.Close()
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
	}
	return cases
}

func TestParseCorpus(t *testing.T) {
	for _, c := range loadCorpus(t) {
		got, err := Parse(c.link)
		if err != nil {
			t.Errorf("%s:%d: %v", c.file, c.line, err)
			continue
		}
		got.Raw = ""
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:%d:\n got %+v\nwant %+v", c.file, c.line, got, c.want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	for _, c := range loadCorpus(t) {
		first, err := Parse(c.link)
		if err != nil {
			t.Errorf("%s:%d: %v", c.file, c.line, err)
			continue
		}
		second, err := Parse(Export(first))
		if err != nil {
			t.Errorf("%s:%d: re-parse: %v", c.file, c.line, err)
			continue
		}
		first.Raw, second.Raw = "", ""
		if !reflect.DeepEqual(first, second) {
			t.Errorf("%s:%d: round trip changed node:\n first %+v\nsecond %+v", c.file, c.line, first, second)
		}
	}
}

func TestOutboundCorpus(t *testing.T) {
	for _, c := range loadCorpus(t) {
		n, err := Parse(c.link)
		if err != nil {
			t.Fatal(err)
		}
		o, err := Outbound(n, "proxy")
		if err != nil {
			if errors.Is(err, ErrUnsupported) || errors.Is(err, ErrInsecureTLS) {
				continue // e.g. SIP003 plugins have no Xray equivalent
			}
			t.Errorf("%s:%d: %v", c.file, c.line, err)
			continue
		}
		if tag, _ := o.Get("tag"); tag != "proxy" {
			t.Errorf("%s:%d: tag = %v", c.file, c.line, tag)
		}
	}
}

// A TCP header (vless headerType, vmess type with net tcp) would change the
// wire format; Outbound refuses it rather than drop it.
func TestOutboundCorpusTCPHeaderUnsupported(t *testing.T) {
	found := 0
	for _, c := range loadCorpus(t) {
		if !strings.HasSuffix(c.want.Remark, "tcp http header") {
			continue
		}
		found++
		n, err := Parse(c.link)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Outbound(n, "proxy")
		if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "tcp header") {
			t.Errorf("%s:%d: err = %v, want ErrUnsupported naming the tcp header", c.file, c.line, err)
		}
	}
	if found != 2 {
		t.Fatalf("found %d tcp-header corpus lines, want 2", found)
	}
}

// TestOutboundBuildsInXray feeds every corpus outbound to xray-core's own
// config loader, so shapes Xray rejects fail here, not at connect time.
func TestOutboundBuildsInXray(t *testing.T) {
	for _, c := range loadCorpus(t) {
		n, err := Parse(c.link)
		if err != nil {
			t.Fatal(err)
		}
		o, err := Outbound(n, "proxy")
		if errors.Is(err, ErrUnsupported) || errors.Is(err, ErrInsecureTLS) {
			continue
		}
		if err != nil {
			t.Errorf("%s:%d: %v", c.file, c.line, err)
			continue
		}
		b, err := ojson.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		var cfg conf.OutboundDetourConfig
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Errorf("%s:%d: xray rejects JSON: %v", c.file, c.line, err)
			continue
		}
		_, err = cfg.Build()
		if n.Scheme == "vless" && (n.Params["security"] == "" || n.Params["security"] == "none") {
			// Xray refuses plaintext vless to a public host already in Build
			// (the corpus uses example.com hosts); that refusal is expected.
			if err == nil || !strings.Contains(err.Error(), "vless without TLS") {
				t.Errorf("%s:%d: want Xray's plaintext-vless refusal, got %v", c.file, c.line, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s:%d (%s): xray Build: %v", c.file, c.line, n.Scheme, err)
		}
	}
}
