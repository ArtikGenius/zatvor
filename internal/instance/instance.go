// Package instance keeps zatvord to one instance per user and lets a second
// launch ask the running one to show its window (K43).
package instance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ArtikGenius/zatvor/internal/api"
	"github.com/ArtikGenius/zatvor/internal/appdir"
)

// forwardTimeout bounds the whole show request of a second launch.
const forwardTimeout = 3 * time.Second

// Acquire takes the single-instance lock called name (see Name). already is
// true when another holder has it; release is then a no-op. Otherwise the
// lock is held until release, which is safe to call more than once. The lock
// is not inherited by child processes.
func Acquire(name string) (release func(), already bool, err error) { return acquire(name) }

// Forward asks the running instance, found through the discovery and token
// files in dirs.Run, to show its window: POST /v1/show with the bearer token
// and the Host the server expects, within 3 s, never through a proxy. The
// discovery file is read before and after the token; if it changed, the
// token may belong to another instance and nothing is sent.
func Forward(dirs appdir.Dirs) error {
	d, err := api.ReadDiscovery(dirs)
	if err != nil {
		return fmt.Errorf("instance: %w", err)
	}
	tok, err := api.ReadToken(dirs)
	if err != nil {
		return fmt.Errorf("instance: %w", err)
	}
	if afterTokenRead != nil {
		afterTokenRead()
	}
	if again, err := api.ReadDiscovery(dirs); err != nil || again != d {
		return errors.New("instance: the discovery file changed while it was read")
	}
	ctx, cancel := context.WithTimeout(context.Background(), forwardTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/show", d.Port), nil)
	if err != nil {
		return fmt.Errorf("instance: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	c := &http.Client{
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("instance: show: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("instance: show: HTTP %d", resp.StatusCode)
	}
	return nil
}

// afterTokenRead is a test hook run between the token read and the second
// discovery read.
var afterTokenRead func()
