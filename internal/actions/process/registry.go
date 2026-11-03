package process

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// The tree registry records the root of every running Tree in a directory
// so that a restarted vm-agent can terminate trees orphaned by a crash
// (spec D4). On Windows the Job Object already dies with its last handle, so
// only Linux needs it; the bookkeeping is platform-neutral.
var (
	registryMu  sync.Mutex
	registryDir string
)

// TrackTrees enables the registry in dir.
func TrackTrees(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	registryMu.Lock()
	registryDir = dir
	registryMu.Unlock()
	return nil
}

func registerTree(pid int) {
	registryMu.Lock()
	dir := registryDir
	registryMu.Unlock()
	if dir == "" || !trackTrees {
		return
	}
	start, err := startTime(pid)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)), []byte(start), 0o600)
}

func unregisterTree(pid int) {
	registryMu.Lock()
	dir := registryDir
	registryMu.Unlock()
	if dir != "" {
		_ = os.Remove(filepath.Join(dir, strconv.Itoa(pid)))
	}
}

// ReapOrphans terminates the trees recorded in dir by a previous run whose
// root is still alive with the same start time, and clears the records. It
// returns the pids whose trees were killed.
func ReapOrphans(dir string) []int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var killed []int
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		pid, err := strconv.Atoi(e.Name())
		data, readErr := os.ReadFile(path)
		_ = os.Remove(path)
		if err != nil || readErr != nil {
			continue
		}
		// A live process with another start time means the pid was reused:
		// leave it alone. With no process at all the group id cannot have been
		// reused while members remain, so any surviving members are ours.
		if current, err := startTime(pid); err == nil && current != strings.TrimSpace(string(data)) {
			continue
		}
		if killGroup(pid) == nil {
			killed = append(killed, pid)
		}
	}
	return killed
}
