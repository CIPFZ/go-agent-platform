//go:build linux

package process

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// IsSameProgram reports whether pid is alive and runs an executable with the
// same file name as this process, which guards lock files against pid reuse.
func IsSameProgram(pid int) bool {
	if pid <= 0 {
		return false
	}
	target, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	return exeName(target) == exeName(self)
}

func exeName(path string) string { return filepath.Base(strings.TrimSuffix(path, " (deleted)")) }
