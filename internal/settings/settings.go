// Package settings keeps the daemon's persisted settings in one JSON file.
// Save writes a temp file in the same directory, fsyncs it and renames it over
// the main file, after copying the current good file to ".bak". Load returns
// the defaults when the file is absent.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ArtikGenius/zatvor/internal/gen"
)

// CurrentVersion is the file format version this build reads and writes.
const CurrentVersion = 1

// Settings are the persisted daemon settings.
type Settings struct {
	Version int `json:"version"`
	// Ports are Xray's listeners; Ports.API is Xray's api inbound.
	Ports         gen.Ports `json:"ports"`
	ActiveProfile string    `json:"activeProfile"`
	DevTools      bool      `json:"devTools"`
	LogLevel      string    `json:"logLevel"`
	UpdateOnStart bool      `json:"updateOnStart"`
	// LocalAPIPort is the port of zatvord's own local HTTP API (ADR 0009),
	// separate from every Xray port. 0, also a file without the field, means
	// none persisted.
	LocalAPIPort int `json:"localApiPort"`
}

// Errors for errors.Is. Messages never hold file contents.
var (
	// ErrCorrupt: the file is empty or not a settings document.
	ErrCorrupt = errors.New("settings: file is damaged")
	// ErrNewerVersion: the file was written by a newer Zatvor.
	ErrNewerVersion = errors.New("settings: written by a newer version")
	// ErrRefuseEmpty: Save was given the zero Settings while a non-empty file
	// exists; it would wipe the user's settings.
	ErrRefuseEmpty = errors.New("settings: refusing to overwrite a non-empty file with empty settings")
)

// Defaults are the settings used when no file exists.
func Defaults() Settings {
	return Settings{Version: CurrentVersion, LogLevel: "warning"}
}

// afterTemp is a test hook run after the temp file is written, before the
// rename.
var afterTemp func() error

// Load reads path. A missing file gives Defaults. It never writes and never
// falls back to ".bak".
func Load(path string) (Settings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	return parse(data)
}

func parse(data []byte) (Settings, error) {
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("%w: not a JSON settings document", ErrCorrupt)
	}
	switch {
	case s.Version > CurrentVersion:
		return Settings{}, fmt.Errorf("%w: version %d", ErrNewerVersion, s.Version)
	case s.Version < 1:
		return Settings{}, fmt.Errorf("%w: missing version", ErrCorrupt)
	}
	return s, nil
}

// Save writes s to path atomically. The current file becomes ".bak" only if it
// is a valid settings file, so a damaged file never replaces a good backup.
// The zero Settings over an existing non-empty file is ErrRefuseEmpty. A zero
// Version is written as CurrentVersion.
func Save(path string, s Settings) error {
	if s.Version > CurrentVersion {
		return fmt.Errorf("%w: version %d", ErrNewerVersion, s.Version)
	}
	old, err := os.ReadFile(path)
	switch {
	case err == nil:
		if s == (Settings{}) && len(bytes.TrimSpace(old)) > 0 {
			return ErrRefuseEmpty
		}
		// A downgrade must not destroy a newer file.
		if _, perr := parse(old); errors.Is(perr, ErrNewerVersion) {
			return perr
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if s.Version == 0 {
		s.Version = CurrentVersion
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("settings: encode: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if old != nil {
		if _, perr := parse(old); perr == nil {
			if err := writeAtomic(dir, path+".bak", old); err != nil {
				return err
			}
		}
	}
	if err := writeAtomic(dir, path, data); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil { // best effort: persist the renames
		d.Sync()
		d.Close()
	}
	return nil
}

// writeAtomic writes data to a temp file in dir, fsyncs it and renames it over
// dest. The hook fires only for the main file's write.
func writeAtomic(dir, dest string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "settings.json.tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error { tmp.Close(); os.Remove(name); return err }
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if afterTemp != nil && filepath.Ext(dest) != ".bak" {
		if err := afterTemp(); err != nil {
			os.Remove(name)
			return err
		}
	}
	if err := os.Rename(name, dest); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
