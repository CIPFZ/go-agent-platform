//go:build windows

package process

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const stillActive = 259

// IsSameProgram reports whether pid is alive and runs an executable with the
// same file name as this process, which guards lock files against pid reuse.
func IsSameProgram(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil || code != stillActive {
		return false
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &size) != nil {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.EqualFold(filepath.Base(windows.UTF16ToString(buf[:size])), filepath.Base(self))
}
