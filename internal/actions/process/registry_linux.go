//go:build linux

package process

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const trackTrees = true

// startTime returns field 22 of /proc/<pid>/stat (start time in clock
// ticks since boot), which identifies a process across pid reuse.
func startTime(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	// The command name may contain spaces; fields resume after ')'.
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", errors.New("malformed stat")
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 20 {
		return "", errors.New("malformed stat")
	}
	return fields[19], nil
}

func killGroup(pgid int) error {
	if pgid <= 1 {
		return syscall.EINVAL
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
