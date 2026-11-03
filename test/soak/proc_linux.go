//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
)

// procStats returns RSS in KiB, open file descriptors and OS threads.
func procStats(pid int) (rss int64, handles, threads int) {
	base := "/proc/" + strconv.Itoa(pid)
	if data, err := os.ReadFile(base + "/status"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
				rss, _ = strconv.ParseInt(strings.Fields(v)[0], 10, 64)
			}
			if v, ok := strings.CutPrefix(line, "Threads:"); ok {
				threads, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		}
	}
	if entries, err := os.ReadDir(base + "/fd"); err == nil {
		handles = len(entries)
	}
	return rss, handles, threads
}
