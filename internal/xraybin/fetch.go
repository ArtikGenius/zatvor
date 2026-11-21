// Package xraybin fetches and verifies the pinned Xray-core release.
package xraybin

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const maxZipBytes = 200 << 20

// DefaultBaseURL returns the GitHub release download base for a pinned version.
func DefaultBaseURL(version string) string {
	return "https://github.com/XTLS/Xray-core/releases/download/" + version + "/"
}

// Fetch downloads the pinned Xray archive for platform, verifies it, and
// extracts the xray binary into destDir. The archive must match both the
// pinned SHA-256 and the SHA2-256 line of the release .dgst file. A baseURL
// of "" uses the GitHub release for pin.Version.
func Fetch(ctx context.Context, client *http.Client, pin Pin, baseURL, platform, destDir string) (string, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL(pin.Version)
	}
	asset, ok := pin.Assets[platform]
	if !ok {
		return "", fmt.Errorf("no pinned asset for platform %q", platform)
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	dgst, err := fetchDgst(ctx, client, baseURL+asset.Zip+".dgst")
	if err != nil {
		return "", fmt.Errorf("%s: dgst: %w", asset.Zip, err)
	}
	if !strings.EqualFold(dgst, asset.SHA256) {
		return "", fmt.Errorf("%s: dgst SHA2-256 %s disagrees with pin %s", asset.Zip, dgst, asset.SHA256)
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	tmpZip, err := os.CreateTemp(destDir, ".tmp-zip-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpZip.Name())
	defer tmpZip.Close()

	got, err := download(ctx, client, baseURL+asset.Zip, tmpZip)
	if err != nil {
		return "", fmt.Errorf("%s: download: %w", asset.Zip, err)
	}
	if !strings.EqualFold(got, asset.SHA256) {
		return "", fmt.Errorf("%s: SHA-256 mismatch: got %s, pinned %s", asset.Zip, got, asset.SHA256)
	}

	exeName := "xray"
	if strings.HasPrefix(platform, "windows-") {
		exeName = "xray.exe"
	}
	return extractExe(tmpZip.Name(), exeName, destDir)
}

func fetchDgst(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 64<<10))
	for sc.Scan() {
		k, v, found := strings.Cut(sc.Text(), "=")
		if found && strings.TrimSpace(k) == "SHA2-256" {
			return strings.ToLower(strings.TrimSpace(v)), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no SHA2-256 line")
}

func download(ctx context.Context, client *http.Client, url string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, maxZipBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxZipBytes {
		return "", fmt.Errorf("archive larger than %d bytes", maxZipBytes)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractExe copies only the named entry (matched at the archive root) out of
// the zip, via a temp file and an atomic rename.
func extractExe(zipPath, exeName, destDir string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if path.Clean(f.Name) != exeName || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		tmp, err := os.CreateTemp(destDir, ".tmp-exe-*")
		if err != nil {
			return "", err
		}
		ok := false
		defer func() {
			if !ok {
				tmp.Close()
				os.Remove(tmp.Name())
			}
		}()
		if _, err := io.Copy(tmp, io.LimitReader(rc, maxZipBytes)); err != nil {
			return "", err
		}
		if err := tmp.Chmod(0o755); err != nil {
			return "", err
		}
		if err := tmp.Close(); err != nil {
			return "", err
		}
		final := filepath.Join(destDir, exeName)
		if err := os.Rename(tmp.Name(), final); err != nil {
			return "", err
		}
		ok = true
		return final, nil
	}
	return "", fmt.Errorf("%s not found in archive", exeName)
}
