package process

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// Info is one process of process/list.
type Info struct {
	PID       int        `json:"pid"`
	PPID      int        `json:"ppid"`
	Name      string     `json:"name"`
	Exe       string     `json:"exe"`
	StartedAt *time.Time `json:"started_at"`
}

// ErrNotFound means the pid does not exist.
var ErrNotFound = errors.New("no such process")

// List returns the processes whose name contains filter (case-insensitive),
// sorted by pid.
func List(filter string) ([]Info, error) {
	all, err := list()
	if err != nil {
		return nil, err
	}
	filter = strings.ToLower(filter)
	out := make([]Info, 0, len(all))
	for _, p := range all {
		if filter == "" || strings.Contains(strings.ToLower(p.Name), filter) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

// Descendants returns pid and every descendant, parents before children.
func Descendants(pid int) ([]int, error) {
	all, err := list()
	if err != nil {
		return nil, err
	}
	children := map[int][]int{}
	for _, p := range all {
		if p.PID != p.PPID {
			children[p.PPID] = append(children[p.PPID], p.PID)
		}
	}
	out := []int{pid}
	seen := map[int]bool{pid: true}
	for i := 0; i < len(out); i++ {
		for _, c := range children[out[i]] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// KillRequest is a validated process/kill request.
type KillRequest struct {
	PID   int
	Tree  bool
	Grace time.Duration
	Force bool
}

// KillResult reports what Kill did.
type KillResult struct {
	PID      int   `json:"pid"`
	Targets  []int `json:"targets"`
	Graceful bool  `json:"graceful"` // every target exited within the grace period
	Forced   []int `json:"forced"`   // targets killed after the grace period
	Exited   bool  `json:"exited"`   // no target remains
}

// Kill terminates a process (and with Tree its descendants) politely first:
// SIGTERM on Linux, WM_CLOSE to its top-level windows on Windows; whatever
// is still alive after Grace is killed (spec 6.5). Force skips the polite
// step. The protected pids (the agent itself) are refused.
func Kill(ctx context.Context, req KillRequest, protected map[int]bool) (*KillResult, error) {
	if !exists(req.PID) {
		return nil, ErrNotFound
	}
	targets := []int{req.PID}
	if req.Tree {
		var err error
		if targets, err = Descendants(req.PID); err != nil {
			return nil, err
		}
	}
	for _, t := range targets {
		if protected[t] {
			return nil, ErrProtected
		}
	}
	res := &KillResult{PID: req.PID, Targets: targets, Forced: []int{}}
	if !req.Force {
		// Children first, so a parent is not woken by its children's deaths
		// into respawning them before it is asked to stop itself.
		for i := len(targets) - 1; i >= 0; i-- {
			_ = terminate(targets[i])
		}
		deadline := time.Now().Add(req.Grace)
		for time.Now().Before(deadline) && anyAlive(targets) {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
		res.Graceful = !anyAlive(targets)
	}
	for _, t := range targets {
		if exists(t) {
			if err := forceKill(t); err == nil {
				res.Forced = append(res.Forced, t)
			}
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for anyAlive(targets) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	res.Exited = !anyAlive(targets)
	return res, nil
}

// ErrProtected refuses to kill vm-agent itself.
var ErrProtected = errors.New("refusing to kill vm-agent itself")

func anyAlive(pids []int) bool {
	for _, p := range pids {
		if exists(p) {
			return true
		}
	}
	return false
}
