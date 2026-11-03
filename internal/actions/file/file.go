// Package file implements the /v1/file endpoints on the local filesystem.
// Paths are absolute; when -root is set every path must resolve, after
// following symbolic links, inside one of the roots (spec section 6.6).
package file

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Limits of spec sections 6.6 and 7.
const (
	ReadMax        int64 = 64 << 20
	DefaultMaxList       = 1000
	MinFreeBytes   int64 = 500 << 20
	DownloadMax    int64 = 1 << 30
	DefaultRetries       = 3
	MaxRedirects         = 10
	tempPrefix           = ".vm-agent-write-"
)

// FS applies the path rules.
type FS struct {
	roots []string // cleaned, symlinks resolved
}

// New returns an FS limited to roots (none means unrestricted).
func New(roots []string) (*FS, error) {
	f := &FS{}
	for _, r := range roots {
		resolved, err := filepath.EvalSymlinks(r)
		if err != nil {
			return nil, fmt.Errorf("-root %s: %w", r, err)
		}
		f.roots = append(f.roots, filepath.Clean(resolved))
	}
	return f, nil
}

// Path validates p and returns it cleaned.
func (f *FS) Path(p string) (string, error) {
	if p == "" || !filepath.IsAbs(p) {
		return "", protocol.Errorf(protocol.CodeInvalidRequest, "path %q must be absolute", p).WithReason(protocol.ReasonPathNotAbsolute)
	}
	clean := filepath.Clean(p)
	if len(f.roots) == 0 {
		return clean, nil
	}
	resolved, err := resolveExisting(clean)
	if err != nil {
		return "", err
	}
	for _, root := range f.roots {
		if within(root, resolved) {
			return clean, nil
		}
	}
	return "", protocol.Errorf(protocol.CodeInvalidRequest, "path %q is outside the allowed roots", p).WithReason(protocol.ReasonOutsideRoot)
}

// resolveExisting follows symlinks of the longest existing prefix of p, so
// a link inside a root cannot point a new file outside it.
func resolveExisting(p string) (string, error) {
	suffix := []string{}
	probe := p
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return p, nil
		}
		suffix = append(suffix, filepath.Base(probe))
		probe = parent
	}
}

func within(root, p string) bool {
	if runtime.GOOS == "windows" {
		root, p = strings.ToLower(root), strings.ToLower(p)
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Content is file data read by Read.
type Content struct {
	Data   []byte
	SHA256 string
	Mtime  time.Time
}

// Read reads up to limit bytes (0 means to the end) from offset. A single
// read returns at most ReadMax bytes; larger files are read in segments.
func (f *FS) Read(p string, offset, limit int64) (*Content, error) {
	if offset < 0 || limit < 0 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "offset and limit must not be negative")
	}
	if limit > ReadMax {
		return nil, protocol.Errorf(protocol.CodeTooLarge, "limit exceeds the %d byte read maximum", ReadMax)
	}
	file, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "%s is a directory", p)
	}
	n := limit
	if n == 0 {
		n = max(info.Size()-offset, 0)
		if n > ReadMax {
			return nil, protocol.Errorf(protocol.CodeTooLarge, "%s has %d bytes from offset %d; read at most %d bytes at a time with offset and limit", p, n, offset, ReadMax)
		}
	}
	buf := make([]byte, n)
	got, err := file.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	buf = buf[:got]
	sum := sha256.Sum256(buf)
	return &Content{Data: buf, SHA256: hex.EncodeToString(sum[:]), Mtime: info.ModTime()}, nil
}

// WriteResult describes a written file.
type WriteResult struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// WriteOptions control Write and Upload.
type WriteOptions struct {
	Overwrite bool
	Mkdir     bool
	SHA256    string // optional expected digest
}

// Write atomically writes data to p.
func (f *FS) Write(p string, data []byte, o WriteOptions) (*WriteResult, error) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if o.SHA256 != "" && !strings.EqualFold(o.SHA256, digest) {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "content sha256 %s does not match the expected %s", digest, o.SHA256).WithReason(protocol.ReasonSHA256Mismatch)
	}
	if err := f.prepareTarget(p, o); err != nil {
		return nil, err
	}
	n, err := WriteAtomic(p, bytes.NewReader(data), o.Overwrite)
	if err != nil {
		return nil, writeError(p, err)
	}
	return &WriteResult{Size: n, SHA256: digest}, nil
}

// prepareTarget creates the parent when asked, checks free space and
// refuses to replace a directory.
func (f *FS) prepareTarget(p string, o WriteOptions) error {
	dir := filepath.Dir(p)
	if o.Mkdir {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	} else if info, err := os.Stat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return protocol.Errorf(protocol.CodeNotFound, "parent directory %s does not exist; set mkdir to create it", dir)
		}
		return err
	} else if !info.IsDir() {
		return protocol.Errorf(protocol.CodeInvalidRequest, "%s is not a directory", dir)
	}
	if info, err := os.Lstat(p); err == nil && info.IsDir() {
		return protocol.Errorf(protocol.CodeConflict, "%s is a directory", p)
	}
	return CheckFreeSpace(dir)
}

func writeError(p string, err error) error {
	if errors.Is(err, fs.ErrExist) {
		return protocol.Errorf(protocol.CodeConflict, "%s already exists; set overwrite to replace it", p)
	}
	return err
}

// CheckFreeSpace returns io_failed with details.reason=disk_full when the
// volume holding p has less than MinFreeBytes available (spec section 7).
func CheckFreeSpace(p string) error {
	free, err := freeSpace(nearestExisting(p))
	if err != nil {
		return nil // unknown free space never blocks a write
	}
	if free < MinFreeBytes {
		return protocol.Errorf(protocol.CodeIOFailed, "only %d MiB free on the volume of %s; at least %d MiB are required", free>>20, p, MinFreeBytes>>20).WithReason(protocol.ReasonDiskFull)
	}
	return nil
}

// freeSpace is replaceable in tests.
var freeSpace = freeBytes

func nearestExisting(p string) string {
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

// Info is the file/stat result.
type Info struct {
	Exists   bool       `json:"exists"`
	Type     string     `json:"type,omitempty"`
	Size     int64      `json:"size"`
	Mtime    *time.Time `json:"mtime"`
	Hidden   bool       `json:"hidden"`
	Readonly bool       `json:"readonly"`
}

func typeOf(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "symlink"
	case m.IsDir():
		return "dir"
	case m.IsRegular():
		return "file"
	default:
		return "other"
	}
}

// Stat describes p without following a final symbolic link.
func (f *FS) Stat(p string) (*Info, error) {
	info, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return &Info{Exists: false}, nil
	}
	if err != nil {
		return nil, err
	}
	mtime := info.ModTime().UTC()
	return &Info{
		Exists:   true,
		Type:     typeOf(info.Mode()),
		Size:     info.Size(),
		Mtime:    &mtime,
		Hidden:   isHidden(p, info),
		Readonly: isReadonly(p, info),
	}, nil
}

// Entry is one file/list item.
type Entry struct {
	Name   string    `json:"name"`
	Path   string    `json:"path"`
	Type   string    `json:"type"`
	Size   int64     `json:"size"`
	Mtime  time.Time `json:"mtime"`
	Hidden bool      `json:"hidden"`
}

// List lists p, recursively when asked, returning at most maxEntries
// entries and whether more existed. Symbolic links to directories are not
// followed.
func (f *FS) List(p string, recursive bool, maxEntries int) ([]Entry, bool, error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		return nil, false, protocol.Errorf(protocol.CodeInvalidRequest, "%s is not a directory", p)
	}
	entries := []Entry{}
	truncated := false
	stop := errors.New("stop")
	add := func(path string, d fs.DirEntry) error {
		if len(entries) >= maxEntries {
			truncated = true
			return stop
		}
		fi, err := d.Info()
		if err != nil {
			return nil // vanished meanwhile
		}
		entries = append(entries, Entry{Name: d.Name(), Path: path, Type: typeOf(fi.Mode()), Size: fi.Size(), Mtime: fi.ModTime().UTC(), Hidden: isHidden(path, fi)})
		return nil
	}
	if !recursive {
		items, err := os.ReadDir(p)
		if err != nil {
			return nil, false, err
		}
		for _, d := range items {
			if add(filepath.Join(p, d.Name()), d) != nil {
				break
			}
		}
		return entries, truncated, nil
	}
	err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == p {
				return err
			}
			return nil // unreadable subtree: skip
		}
		if path == p {
			return nil
		}
		return add(path, d)
	})
	if err != nil && err != stop {
		return nil, false, err
	}
	return entries, truncated, nil
}

// Mkdir creates p and missing parents.
func (f *FS) Mkdir(p string) error {
	if info, err := os.Stat(p); err == nil {
		if info.IsDir() {
			return nil
		}
		return protocol.Errorf(protocol.CodeConflict, "%s exists and is not a directory", p)
	}
	return os.MkdirAll(p, 0o755)
}
