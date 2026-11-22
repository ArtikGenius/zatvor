// Package subfetch performs the one subscription request per user action.
// It never retries; retries belong to the scheduler.
package subfetch

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/ArtikGenius/zatvor/internal/hwid"
	"github.com/ArtikGenius/zatvor/internal/subbody"
	"github.com/ArtikGenius/zatvor/internal/subhdr"
)

// maxBody caps both the wire and the decompressed body.
const maxBody = 16 << 20

const maxRedirects = 5

// Dialer opens a connection; nil in Request means a direct connection.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// Request describes one fetch. HWID headers are copied onto the request as given.
type Request struct {
	URL  string
	UA   string
	HWID http.Header
	Via  Dialer
}

// Problem classifies why a fetch did not produce a usable subscription.
type Problem int

const (
	None Problem = iota
	Network
	HTTPStatus
	EmptyBody
	NotASubscription
	DeviceLimit
	HWIDRequired
	Encrypted
	ClashUnsupported
)

// Result carries no URL: it may be logged.
type Result struct {
	Body    subbody.Result
	Info    subhdr.Info
	Status  int
	Problem Problem
}

// Fetcher issues requests with a fixed timeout and merges concurrent
// identical ones.
type Fetcher struct {
	timeout time.Duration
	tlsCfg  *tls.Config // tests only
	group   singleflight.Group
}

// New returns a Fetcher; production uses 20 seconds.
func New(timeout time.Duration) *Fetcher { return &Fetcher{timeout: timeout} }

// DefaultUA is the UA sent unless the profile says otherwise.
func DefaultUA(version, os string) string { return "Zatvor/" + version + " (" + os + ")" }

// HappCompatibleUA marks us as Happ-compatible without impersonating it.
func HappCompatibleUA(version, os string) string {
	return "Zatvor/" + version + " (" + os + "; happ-compatible)"
}

// HappUA impersonates Happ with the pinned version.
func HappUA(pinned, os string) string {
	name := os
	if os == "windows" {
		name = "Windows"
	}
	return "Happ/" + pinned + "/" + name + "/zatvor"
}

type raw struct {
	status int
	header http.Header
	body   []byte
	tooBig bool
}

// Fetch makes one request. Concurrent calls with the same URL, UA and HWID
// headers share it. The error is non-nil only for Problem Network (or an
// unusable URL) and names only the host.
func (f *Fetcher) Fetch(ctx context.Context, req Request) (Result, error) {
	u, err := url.Parse(req.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return Result{Problem: Network}, errors.New("subscription URL is not a valid http(s) URL")
	}
	host := u.Host
	v, err, _ := f.group.Do(flightKey(req), func() (any, error) {
		return f.do(ctx, req, host)
	})
	if err != nil {
		return Result{Problem: Network}, err
	}
	r := v.(*raw)
	return classify(r), nil
}

func flightKey(req Request) string {
	var b strings.Builder
	b.WriteString(req.URL)
	b.WriteString("\x00")
	b.WriteString(req.UA)
	keys := make([]string, 0, len(req.HWID))
	for k := range req.HWID {
		keys = append(keys, http.CanonicalHeaderKey(k))
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("\x00" + k + "=" + strings.Join(req.HWID.Values(k), "\x01"))
	}
	return b.String()
}

func (f *Fetcher) do(ctx context.Context, req Request, host string) (*raw, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return nil, errors.New("subscription request could not be built")
	}
	for k, vs := range req.HWID {
		for _, v := range vs {
			hr.Header.Add(k, v)
		}
	}
	hr.Header.Set("User-Agent", req.UA)
	hr.Header.Set("Accept-Encoding", "gzip")

	tr := &http.Transport{
		Proxy:               nil, // never pick up system proxy settings
		TLSClientConfig:     f.tlsCfg,
		TLSHandshakeTimeout: f.timeout,
		DisableKeepAlives:   true,
	}
	if req.Via != nil {
		tr.DialContext = req.Via
	} else {
		tr.DialContext = (&net.Dialer{Timeout: f.timeout}).DialContext
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("more than %d redirects from %s", maxRedirects, host)
			}
			prev := via[len(via)-1]
			if prev.URL.Scheme == "https" && next.URL.Scheme != "https" {
				return fmt.Errorf("redirect from %s to %s downgrades https to http", prev.URL.Host, next.URL.Host)
			}
			// HWID identifies the device to the subscription host only.
			if hwid.NormalizeHost(next.URL.Host) != hwid.NormalizeHost(via[0].URL.Host) {
				for k := range req.HWID {
					next.Header.Del(k)
				}
			}
			return nil
		},
	}
	resp, err := client.Do(hr)
	if err != nil {
		return nil, networkError(err, host)
	}
	defer resp.Body.Close()

	var rd io.Reader = resp.Body
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		zr, err := gzip.NewReader(io.LimitReader(resp.Body, maxBody+1))
		if err != nil {
			return &raw{status: resp.StatusCode, header: resp.Header.Clone(), body: []byte("\x00")}, nil
		}
		defer zr.Close()
		rd = zr
	}
	data, err := io.ReadAll(io.LimitReader(rd, maxBody+1))
	if err != nil && ctx.Err() != nil {
		return nil, networkError(err, host)
	}
	if err != nil {
		// Truncated or corrupt stream: not a subscription.
		return &raw{status: resp.StatusCode, header: resp.Header.Clone(), body: []byte("\x00")}, nil
	}
	if len(data) > maxBody {
		return &raw{status: resp.StatusCode, header: resp.Header.Clone(), tooBig: true}, nil
	}
	return &raw{status: resp.StatusCode, header: resp.Header.Clone(), body: data}, nil
}

// networkError drops the *url.Error wrapper, which prints the full URL.
func networkError(err error, host string) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("request to %s timed out", host)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("request to %s canceled", host)
	}
	return fmt.Errorf("request to %s failed: %v", host, err)
}

func classify(r *raw) Result {
	res := Result{Status: r.status}
	if r.tooBig {
		res.Problem = NotASubscription
		return res
	}
	res.Body = subbody.Decode(r.body)
	res.Info = subhdr.Parse(r.header, res.Body.InlineHeaders, r.header.Get("Content-Disposition"))
	switch {
	case res.Info.HWIDLimitReached:
		res.Problem = DeviceLimit
	case res.Body.Kind == subbody.Empty && res.Info.HWIDNotSupported:
		res.Problem = HWIDRequired
	case r.status < 200 || r.status > 299:
		res.Problem = HTTPStatus
	case res.Body.Kind == subbody.Encrypted:
		res.Problem = Encrypted
	case res.Body.Kind == subbody.Clash:
		res.Problem = ClashUnsupported
	case res.Body.Kind == subbody.Empty:
		res.Problem = EmptyBody
	case res.Body.Kind == subbody.Unknown:
		res.Problem = NotASubscription
	}
	return res
}
