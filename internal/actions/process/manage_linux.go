//go:build linux

package process

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var (
	bootOnce sync.Once
	bootTime time.Time
	clkTck   = 100.0 // USER_HZ, fixed at 100 on every Linux ABI Go supports
)

func boot() time.Time {
	bootOnce.Do(func() {
		var info unix.Sysinfo_t
		if unix.Sysinfo(&info) == nil {
			bootTime = time.Now().Add(-time.Duration(info.Uptime) * time.Second).Truncate(time.Second)
		}
	})
	return bootTime
}

func list() ([]Info, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if info, ok := readProc(pid); ok {
			out = append(out, info)
		}
	}
	return out, nil
}

func readProc(pid int) (Info, bool) {
	base := "/proc/" + strconv.Itoa(pid)
	data, err := os.ReadFile(base + "/stat")
	if err != nil {
		return Info{}, false
	}
	s := string(data)
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return Info{}, false
	}
	fields := strings.Fields(s[close+1:])
	if len(fields) < 20 {
		return Info{}, false
	}
	info := Info{PID: pid, Name: s[open+1 : close]}
	info.PPID, _ = strconv.Atoi(fields[1])
	if ticks, err := strconv.ParseFloat(fields[19], 64); err == nil && !boot().IsZero() {
		t := boot().Add(time.Duration(ticks / clkTck * float64(time.Second))).UTC()
		info.StartedAt = &t
	}
	if exe, err := os.Readlink(base + "/exe"); err == nil {
		info.Exe = strings.TrimSuffix(exe, " (deleted)")
		// comm is truncated to 15 bytes; prefer the executable's name.
		if len(info.Name) == 15 && strings.HasPrefix(filepath.Base(info.Exe), info.Name) {
			info.Name = filepath.Base(info.Exe)
		}
	}
	return info, true
}

func exists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil && err != syscall.EPERM {
		return false
	}
	// Zombies are dead for every practical purpose.
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(data)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return s[i+2] != 'Z'
	}
	return true
}

func terminate(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

func forceKill(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }
