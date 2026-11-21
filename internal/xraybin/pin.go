package xraybin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Asset is one pinned release archive.
type Asset struct {
	Zip    string `json:"zip"`
	SHA256 string `json:"sha256"`
}

// Pin is the content of third_party/xray/pin.json.
type Pin struct {
	Version string           `json:"version"`
	Assets  map[string]Asset `json:"assets"`
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
// go.mod and returns third_party/xray/pin.json there.
func RepoPinPath() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "third_party", "xray", "pin.json"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above working directory")
		}
		dir = parent
	}
}
