//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

var (
	psapi                    = windows.NewLazySystemDLL("psapi.dll")
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetProcessMemoryInfo = psapi.NewProc("GetProcessMemoryInfo")
	procGetHandleCount       = kernel32.NewProc("GetProcessHandleCount")
)

// procStats returns the working set in KiB, the handle count and threads
// (threads are not sampled on Windows and report 0).
func procStats(pid int) (rss int64, handles, threads int) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return 0, 0, 0
	}
	defer windows.CloseHandle(h)
	var mc processMemoryCounters
	mc.cb = uint32(unsafe.Sizeof(mc))
	if r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&mc)), uintptr(mc.cb)); r != 0 {
		rss = int64(mc.workingSetSize / 1024)
	}
	var n uint32
	if r, _, _ := procGetHandleCount.Call(uintptr(h), uintptr(unsafe.Pointer(&n))); r != 0 {
		handles = int(n)
	}
	return rss, handles, 0
}
