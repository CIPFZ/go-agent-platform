package exec

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// lookPath resolves a bare program name against an explicit PATH value, the
// way the child's own environment would. Names containing a separator are
// left to os/exec.
func lookPath(name, path string) (string, error) {
	if strings.ContainsAny(name, `/\`) {
		return "", errors.New("not a bare program name")
	}
	exts := []string{""}
	if runtime.GOOS == "windows" {
		exts = []string{".com", ".exe", ".bat", ".cmd"}
		if ext := filepath.Ext(name); ext != "" {
			exts = append([]string{""}, exts...)
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		for _, ext := range exts {
			candidate := filepath.Join(dir, name+ext)
			info, err := os.Stat(candidate)
			if err != nil || info.IsDir() {
				continue
			}
			if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
				continue
			}
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}
