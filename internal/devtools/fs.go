package devtools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Version identifies the content of a file as read: a missing file has
// Exists false. Two reads with equal Versions saw the same bytes.
type Version struct {
	Exists  bool
	Size    int64
	ModTime time.Time
	Sum     [sha256.Size]byte

	perm fs.FileMode // not compared
}

func (v Version) equal(o Version) bool {
	return v.Exists == o.Exists && v.Size == o.Size && v.ModTime.Equal(o.ModTime) && v.Sum == o.Sum
}

// WriteOpts controls FS.WriteFile.
type WriteOpts struct {
	Perm   fs.FileMode // mode of a new file; an existing file keeps its own
	Expect Version     // the Version the caller read; the write fails with ErrChanged otherwise
	Backup bool        // keep a one-time copy of the old file at <file>.zatvor.bak
	// Delete removes the file instead of writing it, with the same Expect
	// checks. A symlink is never deleted: data is written through it.
	Delete bool
}

// FS is the file access devtools needs. The real one is OSFS; in safe mode
// the daemon passes a Recorder, which reads the real files and writes
// nothing.
type FS interface {
	// ReadFile reads a file through symlinks. A missing file is not an
	// error: it returns nil data and a zero Version.
	ReadFile(path string) ([]byte, Version, error)
	// DirExists reports whether path is an existing directory.
	DirExists(path string) bool
	// WriteFile replaces the file atomically, through symlinks: the link
	// stays a link and its final target gets the new content. If the file
	// no longer matches o.Expect, nothing is written and the error wraps
	// ErrChanged. Any error means the file was not replaced.
	WriteFile(path string, data []byte, o WriteOpts) error
	// Remove deletes a file; a missing file is not an error.
	Remove(path string) error
	// MkdirAll creates a directory and its parents.
	MkdirAll(path string) error
}

// ErrChanged reports that a file changed between our read and our write.
var ErrChanged = errors.New("devtools: the file changed while it was being updated")

// errBrokenLink reports a symlink whose target does not exist; we neither
// read through it nor replace it.
var errBrokenLink = errors.New("devtools: broken symlink")

type osFS struct {
	beforeRename func(path string) // test hook, given the caller's path: runs between the temp write and the last check
}

// OSFS returns the FS of the real file system.
func OSFS() FS { return &osFS{} }

func versionOf(b []byte, fi fs.FileInfo) Version {
	return Version{Exists: true, Size: fi.Size(), ModTime: fi.ModTime(), Sum: sha256.Sum256(b), perm: fi.Mode().Perm()}
}

func (*osFS) ReadFile(path string) ([]byte, Version, error) { return readVersion(path) }

func readVersion(path string) ([]byte, Version, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return nil, Version{}, fmt.Errorf("%s: %w", path, errBrokenLink)
		}
		return nil, Version{}, nil
	}
	if err != nil {
		return nil, Version{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, Version{}, err
	}
	if !fi.Mode().IsRegular() {
		return nil, Version{}, fmt.Errorf("devtools: %s is not a regular file", path)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, Version{}, err
	}
	return b, versionOf(b, fi), nil
}

func (*osFS) DirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// remove deletes path after the same two Expect checks as a write.
func (o *osFS) remove(path string, expect Version) error {
	for i := range 2 {
		if i == 1 && o.beforeRename != nil {
			o.beforeRename(path)
		}
		if _, v, err := readVersion(path); err != nil {
			return err
		} else if !v.equal(expect) {
			return fmt.Errorf("%s: %w", path, ErrChanged)
		}
	}
	return o.Remove(path)
}

func (*osFS) Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (*osFS) MkdirAll(path string) error { return os.MkdirAll(path, 0o755) }

// resolve returns the file a write must replace: the final target of a
// symlink chain, or path itself when nothing exists there yet.
func resolve(path string) (string, error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return path, nil
	} else if err != nil {
		return "", err
	}
	r, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w: %v", path, errBrokenLink, err)
	}
	return r, nil
}

// WriteFile writes a temp file next to the resolved target, fsyncs it,
// checks size, mtime and SHA-256 against o.Expect once more and renames it
// over the target. The SHA-256 makes the check content-based, so a coarse
// mtime cannot hide a racing write. What remains is the short window
// between that last check and the rename; an editor saving inside it
// would lose its write.
//
// The rename makes a new file: the mode bits are copied, but on Windows an
// explicit ACL, the hidden/read-only/system attributes and alternate data
// streams of the old file are not (the new one inherits the directory's
// ACL; a read-only target makes the rename fail, writing nothing), and a
// hard link to the old file keeps the old content. Symlinks are followed,
// so the link stays a link.
func (o *osFS) WriteFile(path string, data []byte, w WriteOpts) error {
	target, err := resolve(path)
	if err != nil {
		return err
	}
	if w.Delete {
		// Decided on the path itself: EvalSymlinks also respells plain
		// paths (/var → /private/var, 8.3 names → long names).
		if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink == 0 {
			return o.remove(path, w.Expect)
		}
	}
	cur, ver, err := readVersion(target)
	if err != nil {
		return err
	}
	if !ver.equal(w.Expect) {
		return fmt.Errorf("%s: %w", path, ErrChanged)
	}
	mode := w.Perm
	if ver.Exists {
		mode = ver.perm
		if w.Backup {
			if err := backup(target, cur, mode); err != nil {
				return err
			}
		}
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".zatvor-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil && runtime.GOOS != "windows" {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if o.beforeRename != nil {
		o.beforeRename(path)
	}
	if _, again, err := readVersion(target); err != nil || !again.equal(w.Expect) {
		os.Remove(name)
		if err != nil {
			return err
		}
		return fmt.Errorf("%s: %w", path, ErrChanged)
	}
	if err := os.Rename(name, target); err != nil { // MoveFileEx(REPLACE_EXISTING) on Windows
		os.Remove(name)
		return err
	}
	syncDir(dir) // best effort: the rename happened, so the write did
	return nil
}

// backup writes <target>.zatvor.bak once: an existing backup is the
// oldest copy we know of and is never replaced.
func backup(target string, data []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(target+".zatvor.bak", os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return // directories cannot be fsynced there
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// Recorder is the safe-mode FS. It reads the real files but never writes:
// each change is appended to w as a JSON line {"ts","op","args"} and kept
// in memory, so later reads in this process see the recorded state. File
// contents are not logged (settings files can hold secrets): only the path,
// the size and the SHA-256 of what would be written.
type Recorder struct {
	mu    sync.Mutex
	w     io.Writer
	files map[string][]byte // recorded content; nil value = removed
	dirs  map[string]bool
}

// NewRecorder returns a Recorder that logs to w.
func NewRecorder(w io.Writer) *Recorder {
	return &Recorder{w: w, files: map[string][]byte{}, dirs: map[string]bool{}}
}

type recWrite struct {
	Path   string `json:"path"`
	Bytes  int    `json:"bytes,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Backup bool   `json:"backup,omitempty"`
}

func (r *Recorder) record(op string, args any) error {
	b, err := json.Marshal(struct {
		TS   string `json:"ts"`
		Op   string `json:"op"`
		Args any    `json:"args"`
	}{time.Now().UTC().Format(time.RFC3339Nano), op, args})
	if err != nil {
		return err
	}
	_, err = r.w.Write(append(b, '\n'))
	return err
}

func (r *Recorder) read(path string) ([]byte, Version, error) {
	if b, ok := r.files[path]; ok {
		if b == nil {
			return nil, Version{}, nil
		}
		return bytes.Clone(b), Version{Exists: true, Size: int64(len(b)), Sum: sha256.Sum256(b)}, nil
	}
	return readVersion(path)
}

// ReadFile returns the recorded content, else the real file's.
func (r *Recorder) ReadFile(path string) ([]byte, Version, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.read(path)
}

// DirExists reports a recorded or a real directory.
func (r *Recorder) DirExists(path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dirs[path] || (&osFS{}).DirExists(path)
}

// WriteFile records the write after the same Expect check as OSFS.
func (r *Recorder) WriteFile(path string, data []byte, w WriteOpts) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ver, err := r.read(path)
	if err != nil {
		return err
	}
	if !ver.equal(w.Expect) {
		return fmt.Errorf("%s: %w", path, ErrChanged)
	}
	if w.Delete {
		if err := r.record("file.remove", recWrite{Path: path}); err != nil {
			return err
		}
		r.files[path] = nil
		return nil
	}
	sum := sha256.Sum256(data)
	if err := r.record("file.write", recWrite{Path: path, Bytes: len(data), SHA256: hex.EncodeToString(sum[:]), Backup: w.Backup && ver.Exists}); err != nil {
		return err
	}
	r.files[path] = bytes.Clone(data)
	if r.files[path] == nil {
		r.files[path] = []byte{}
	}
	return nil
}

// Remove records the removal.
func (r *Recorder) Remove(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.record("file.remove", recWrite{Path: path}); err != nil {
		return err
	}
	r.files[path] = nil
	return nil
}

// MkdirAll records the directory.
func (r *Recorder) MkdirAll(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.record("dir.create", recWrite{Path: path}); err != nil {
		return err
	}
	r.dirs[path] = true
	return nil
}
