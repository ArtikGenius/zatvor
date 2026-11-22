package assets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// File names of the bundled dat files inside the bundled directory.
const (
	RoscomGeoIP   = "roscomvpn-geoip.dat"
	RoscomGeoSite = "roscomvpn-geosite.dat"
	V2flyGeoIP    = "v2fly-geoip.dat"
	V2flyGeoSite  = "v2fly-geosite.dat"
	// SourcesFile lists the pinned URL and SHA-256 of each bundled file.
	SourcesFile = "sources.json"
)

// PinFile is one pinned download.
type PinFile struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	// Aliases are other URLs serving the same bytes (for example the jsDelivr
	// form Happ profiles use). A profile naming one matches by URL.
	Aliases []string `json:"aliases,omitempty"`
}

// PinSet is the pin of one publisher.
type PinSet struct {
	Tag     string  `json:"tag"`
	GeoIP   PinFile `json:"geoip"`
	GeoSite PinFile `json:"geosite"`
}

// Pin is the content of third_party/geo/pin.json.
type Pin struct {
	Roscomvpn PinSet `json:"roscomvpn"`
	V2fly     PinSet `json:"v2fly"`
}

// Files maps each bundled file name to its pin.
func (p Pin) Files() map[string]PinFile {
	return map[string]PinFile{
		RoscomGeoIP:   p.Roscomvpn.GeoIP,
		RoscomGeoSite: p.Roscomvpn.GeoSite,
		V2flyGeoIP:    p.V2fly.GeoIP,
		V2flyGeoSite:  p.V2fly.GeoSite,
	}
}

// LoadPin reads and parses a pin file.
func LoadPin(file string) (Pin, error) {
	var p Pin
	b, err := os.ReadFile(file)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("parse %s: %w", file, err)
	}
	return p, nil
}

// RepoPinPath walks up from the working directory to the directory holding
// go.mod and returns third_party/geo/pin.json there.
func RepoPinPath() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "third_party", "geo", "pin.json"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above working directory")
		}
		dir = parent
	}
}
