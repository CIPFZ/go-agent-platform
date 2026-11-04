//go:build windows

package process

import (
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func list() ([]Info, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var out []Info
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		info := Info{PID: int(entry.ProcessID), PPID: int(entry.ParentProcessID), Name: windows.UTF16ToString(entry.ExeFile[:])}
		if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID); err == nil {
			buf := make([]uint16, windows.MAX_LONG_PATH)
			size := uint32(len(buf))
			if windows.QueryFullProcessImageName(h, 0, &buf[0], &size) == nil {
				info.Exe = windows.UTF16ToString(buf[:size])
			}
			var created, exit, kernel, user windows.Filetime
			if windows.GetProcessTimes(h, &created, &exit, &kernel, &user) == nil {
				t := time.Unix(0, created.Nanoseconds()).UTC()
				info.StartedAt = &t
			}
			windows.CloseHandle(h)
		}
		out = append(out, info)
	}
	return out, nil
}

const stillActiveCode = 259

func exists(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Access denied still means the process exists.
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == stillActiveCode
}

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")

	closeMu     sync.Mutex
	closeTarget uint32
	closeSent   int
	closeEnum   = windows.NewCallback(func(hwnd, lparam uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == closeTarget {
			if visible, _, _ := procIsWindowVisible.Call(hwnd); visible != 0 {
				procPostMessageW.Call(hwnd, 0x0010 /* WM_CLOSE */, 0, 0)
				closeSent++
			}
		}
		return 1
	})
)

// terminate posts WM_CLOSE to the process's visible top-level windows; a
// process without windows is left for the forced step (spec 6.5).
func terminate(pid int) error {
	closeMu.Lock()
	defer closeMu.Unlock()
	closeTarget, closeSent = uint32(pid), 0
	if err := procEnumWindows.Find(); err != nil {
		return err
	}
	procEnumWindows.Call(closeEnum, 0)
	return nil
}

func forceKill(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}
