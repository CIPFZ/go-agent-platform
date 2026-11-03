package file

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func code(err error) (protocol.Code, string) {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		r, _ := perr.Details["reason"].(string)
		return perr.Code, r
	}
	return "", ""
}

func TestPathRules(t *testing.T) {
	f, _ := New(nil)
	if _, err := f.Path("relative/x"); err == nil {
		t.Fatal("relative path accepted")
	}
	if c, r := code(func() error { _, err := f.Path("rel"); return err }()); c != protocol.CodeInvalidRequest || r != protocol.ReasonPathNotAbsolute {
		t.Fatalf("got %s/%s", c, r)
	}
	abs := filepath.Join(t.TempDir(), "a", "..", "b")
	if p, err := f.Path(abs); err != nil || p != filepath.Clean(abs) {
		t.Fatalf("unrestricted = %q, %v", p, err)
	}
}

func TestRootContainmentFollowsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "escape"))
	f, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{filepath.Join(root, "x.txt"), filepath.Join(root, "new", "deep", "file"), root} {
		if _, err := f.Path(ok); err != nil {
			t.Errorf("%s rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{filepath.Join(outside, "x"), filepath.Join(root, "escape", "x.txt"), filepath.Join(root, "..", "sibling")} {
		_, err := f.Path(bad)
		if c, r := code(err); c != protocol.CodeInvalidRequest || r != protocol.ReasonOutsideRoot {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestReadSegmentsAndLimits(t *testing.T) {
	f, _ := New(nil)
	p := filepath.Join(t.TempDir(), "data.bin")
	os.WriteFile(p, []byte("0123456789"), 0o600)
	c, err := f.Read(p, 3, 4)
	if err != nil || string(c.Data) != "3456" || len(c.SHA256) != 64 || c.Mtime.IsZero() {
		t.Fatalf("segment = %+v, %v", c, err)
	}
	if c, _ := f.Read(p, 8, 0); string(c.Data) != "89" {
		t.Fatalf("to end = %q", c.Data)
	}
	if c, _ := f.Read(p, 100, 0); len(c.Data) != 0 {
		t.Fatalf("past end = %q", c.Data)
	}
	if _, err := f.Read(p, 0, ReadMax+1); func() protocol.Code { c, _ := code(err); return c }() != protocol.CodeTooLarge {
		t.Fatalf("over limit err = %v", err)
	}
	big := filepath.Join(t.TempDir(), "big")
	fh, _ := os.Create(big)
	fh.Truncate(ReadMax + 1)
	fh.Close()
	if _, err := f.Read(big, 0, 0); func() protocol.Code { c, _ := code(err); return c }() != protocol.CodeTooLarge {
		t.Fatalf("whole big file err = %v", err)
	}
	if c, err := f.Read(big, ReadMax, 0); err != nil || len(c.Data) != 1 {
		t.Fatalf("tail segment = %v", err)
	}
	if _, err := f.Read(filepath.Dir(p), 0, 0); err == nil {
		t.Fatal("directory read")
	}
	if _, err := f.Read(filepath.Join(t.TempDir(), "missing"), 0, 0); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestWriteRules(t *testing.T) {
	f, _ := New(nil)
	dir := t.TempDir()
	p := filepath.Join(dir, "out.txt")
	res, err := f.Write(p, []byte("hello"), WriteOptions{})
	if err != nil || res.Size != 5 || res.SHA256 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("write = %+v, %v", res, err)
	}
	if _, err := f.Write(p, []byte("again"), WriteOptions{}); func() protocol.Code { c, _ := code(err); return c }() != protocol.CodeConflict {
		t.Fatalf("no-overwrite err = %v", err)
	}
	if _, err := f.Write(p, []byte("again"), WriteOptions{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != "again" {
		t.Fatalf("content = %q", data)
	}
	_, err = f.Write(p, []byte("x"), WriteOptions{Overwrite: true, SHA256: strings.Repeat("0", 64)})
	if c, r := code(err); c != protocol.CodeInvalidRequest || r != protocol.ReasonSHA256Mismatch {
		t.Fatalf("mismatch err = %v", err)
	}
	if data, _ := os.ReadFile(p); string(data) != "again" {
		t.Fatal("mismatched write modified the file")
	}
	nested := filepath.Join(dir, "a", "b", "c.txt")
	if _, err := f.Write(nested, []byte("x"), WriteOptions{}); func() protocol.Code { c, _ := code(err); return c }() != protocol.CodeNotFound {
		t.Fatalf("missing parent err = %v", err)
	}
	if _, err := f.Write(nested, []byte("x"), WriteOptions{Mkdir: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(dir, []byte("x"), WriteOptions{Overwrite: true}); func() protocol.Code { c, _ := code(err); return c }() != protocol.CodeConflict {
		t.Fatalf("directory target err = %v", err)
	}
}

func TestStatAndList(t *testing.T) {
	f, _ := New(nil)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("aaa"), 0o600)
	os.WriteFile(filepath.Join(dir, ".hidden"), nil, 0o600)
	os.MkdirAll(filepath.Join(dir, "sub", "deeper"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "deeper", "z"), []byte("z"), 0o600)

	info, err := f.Stat(filepath.Join(dir, "a.txt"))
	if err != nil || !info.Exists || info.Type != "file" || info.Size != 3 || info.Mtime == nil || info.Hidden {
		t.Fatalf("stat = %+v, %v", info, err)
	}
	if info, _ := f.Stat(filepath.Join(dir, "nope")); info.Exists {
		t.Fatal("missing path exists")
	}
	if runtime.GOOS != "windows" {
		if info, _ := f.Stat(filepath.Join(dir, ".hidden")); !info.Hidden {
			t.Fatal("dot file not hidden")
		}
		os.Symlink("a.txt", filepath.Join(dir, "link"))
		if info, _ := f.Stat(filepath.Join(dir, "link")); info.Type != "symlink" {
			t.Fatalf("link type = %s", info.Type)
		}
	}
	entries, truncated, err := f.List(dir, false, 100)
	if err != nil || truncated {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, e := range entries {
		names[e.Name] = e.Type
		if e.Path != filepath.Join(dir, e.Name) {
			t.Fatalf("entry path %s", e.Path)
		}
	}
	if names["a.txt"] != "file" || names["sub"] != "dir" {
		t.Fatalf("entries = %v", names)
	}
	all, _, _ := f.List(dir, true, 100)
	found := false
	for _, e := range all {
		if e.Path == filepath.Join(dir, "sub", "deeper", "z") {
			found = true
		}
	}
	if !found {
		t.Fatal("recursive list missed a nested file")
	}
	two, truncated, _ := f.List(dir, true, 2)
	if len(two) != 2 || !truncated {
		t.Fatalf("limited = %d truncated=%v", len(two), truncated)
	}
	if _, _, err := f.List(filepath.Join(dir, "a.txt"), false, 10); err == nil {
		t.Fatal("listed a file")
	}
}

func TestMkdir(t *testing.T) {
	f, _ := New(nil)
	dir := filepath.Join(t.TempDir(), "x", "y")
	if err := f.Mkdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := f.Mkdir(dir); err != nil {
		t.Fatal("existing directory must be fine")
	}
	file := filepath.Join(dir, "f")
	os.WriteFile(file, nil, 0o600)
	if c, _ := code(f.Mkdir(file)); c != protocol.CodeConflict {
		t.Fatal("mkdir over a file")
	}
}

func TestKnownDirs(t *testing.T) {
	dirs, err := KnownDirs()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"home", "desktop", "documents", "downloads", "pictures", "temp"} {
		if !filepath.IsAbs(dirs[k]) {
			t.Errorf("%s = %q", k, dirs[k])
		}
	}
}

func TestWriteRefusedBelowFreeSpaceFloor(t *testing.T) {
	saved := freeSpace
	defer func() { freeSpace = saved }()
	freeSpace = func(string) (int64, error) { return MinFreeBytes - 1, nil }
	f, _ := New(nil)
	p := filepath.Join(t.TempDir(), "x")
	_, err := f.Write(p, []byte("x"), WriteOptions{})
	if c, r := code(err); c != protocol.CodeIOFailed || r != protocol.ReasonDiskFull {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("file written despite the floor")
	}
	freeSpace = func(string) (int64, error) { return MinFreeBytes, nil }
	if _, err := f.Write(p, []byte("x"), WriteOptions{}); err != nil {
		t.Fatal(err)
	}
}
