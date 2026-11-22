// Command geofetch downloads the pinned geo data files, verifies every
// SHA-256 against third_party/geo/pin.json and writes them, with
// sources.json, into -dest. A mismatch is refused.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ArtikGenius/zatvor/internal/geo/assets"
)

const maxFile = 64 << 20

func main() {
	dest := flag.String("dest", "", "destination directory, e.g. third_party/geo/files")
	pinFile := flag.String("pin", "", "path to pin.json (default: repo third_party/geo/pin.json)")
	flag.Parse()
	if *dest == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*dest, *pinFile); err != nil {
		fmt.Fprintln(os.Stderr, "geofetch:", err)
		os.Exit(1)
	}
}

func run(dest, pinFile string) error {
	if pinFile == "" {
		p, err := assets.RepoPinPath()
		if err != nil {
			return err
		}
		pinFile = p
	}
	pin, err := assets.LoadPin(pinFile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	files := pin.Files()
	for name, pf := range files {
		if err := fetchOne(ctx, filepath.Join(dest, name), pf); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		fmt.Println(filepath.Join(dest, name))
	}
	b, err := json.MarshalIndent(files, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dest, assets.SourcesFile), b, 0o644)
}

func fileSHA(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func fetchOne(ctx context.Context, path string, pf assets.PinFile) error {
	if pf.URL == "" || len(pf.SHA256) != 64 {
		return fmt.Errorf("incomplete pin")
	}
	if fileSHA(path) == pf.SHA256 {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pf.URL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", pf.URL, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFile+1))
	if err != nil {
		return err
	}
	if len(b) > maxFile {
		return fmt.Errorf("file exceeds %d bytes", maxFile)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != pf.SHA256 {
		return fmt.Errorf("SHA-256 mismatch: got %s, pinned %s", got, pf.SHA256)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
