//go:build linux

package file

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// isHidden follows the Unix convention of a leading dot.
func isHidden(p string, _ fs.FileInfo) bool { return strings.HasPrefix(filepath.Base(p), ".") }

// isReadonly reports whether the agent user cannot write p.
func isReadonly(p string, _ fs.FileInfo) bool { return unix.Access(p, unix.W_OK) != nil }

func freeBytes(p string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(p, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// KnownDirs resolves the special directories from the xdg-user-dirs
// configuration ($XDG_CONFIG_HOME/user-dirs.dirs), defaulting to the usual
// names under the home directory (spec section 10).
func KnownDirs() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dirs := map[string]string{
		"home":      home,
		"desktop":   filepath.Join(home, "Desktop"),
		"documents": filepath.Join(home, "Documents"),
		"downloads": filepath.Join(home, "Downloads"),
		"pictures":  filepath.Join(home, "Pictures"),
		"temp":      os.TempDir(),
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	f, err := os.Open(filepath.Join(cfg, "user-dirs.dirs"))
	if err != nil {
		return dirs, nil
	}
	defer f.Close()
	keys := map[string]string{"XDG_DESKTOP_DIR": "desktop", "XDG_DOCUMENTS_DIR": "documents", "XDG_DOWNLOAD_DIR": "downloads", "XDG_PICTURES_DIR": "pictures"}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		name, known := keys[strings.TrimSpace(k)]
		if !ok || !known {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		v = strings.Replace(v, "$HOME", home, 1)
		if filepath.IsAbs(v) {
			dirs[name] = filepath.Clean(v)
		}
	}
	return dirs, nil
}

func isWindowsNotSameDevice(error) bool { return false }

func samePath(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }
