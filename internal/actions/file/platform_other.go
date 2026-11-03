//go:build !linux && !windows

package file

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func isHidden(p string, _ fs.FileInfo) bool { return strings.HasPrefix(filepath.Base(p), ".") }

func isReadonly(_ string, info fs.FileInfo) bool { return info.Mode().Perm()&0o200 == 0 }

func freeBytes(string) (int64, error) { return 0, errors.New("not supported") }

// KnownDirs returns the home-based defaults.
func KnownDirs() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"home": home, "desktop": filepath.Join(home, "Desktop"), "documents": filepath.Join(home, "Documents"),
		"downloads": filepath.Join(home, "Downloads"), "pictures": filepath.Join(home, "Pictures"), "temp": os.TempDir(),
	}, nil
}
