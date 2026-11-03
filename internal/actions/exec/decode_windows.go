//go:build windows

package exec

import (
	"golang.org/x/sys/windows"
)

const cpUTF8 = 65001

var procGetOEMCP = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetOEMCP")

// decodeCodePage decodes b from the console output code page, or the OEM
// code page when vm-agent has no console.
func decodeCodePage(b []byte) (string, bool) {
	cp, _ := windows.GetConsoleOutputCP()
	if cp == 0 {
		if err := procGetOEMCP.Find(); err == nil {
			r, _, _ := procGetOEMCP.Call()
			cp = uint32(r)
		}
	}
	if cp == 0 || cp == cpUTF8 || len(b) == 0 {
		return "", false
	}
	n, err := windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n == 0 {
		return "", false
	}
	buf := make([]uint16, n)
	if _, err := windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), &buf[0], n); err != nil {
		return "", false
	}
	return windows.UTF16ToString(buf), true
}
