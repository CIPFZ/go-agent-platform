// Package policy implements the optional command allowlist and denylist of
// spec sections 4.3 and 8. Both lists are disabled unless their files are
// configured; a denied command is reported as invalid_request with
// details.reason=policy_denied.
package policy

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrDenied is returned (wrapped) when a command is rejected.
var ErrDenied = errors.New("command denied by policy")

// Policy holds the loaded lists. The zero value allows everything.
type Policy struct {
	allow list
	deny  list
}

type list struct {
	names map[string]bool
	paths map[string]bool
}

func (l list) empty() bool { return len(l.names) == 0 && len(l.paths) == 0 }

// Load reads the allowlist and denylist files; an empty path disables that
// list. Each non-empty line not starting with '#' is an executable name or an
// absolute path.
func Load(allowFile, denyFile string) (*Policy, error) {
	p := &Policy{}
	var err error
	if p.allow, err = loadList(allowFile); err != nil {
		return nil, fmt.Errorf("load allowlist: %w", err)
	}
	if p.deny, err = loadList(denyFile); err != nil {
		return nil, fmt.Errorf("load denylist: %w", err)
	}
	return p, nil
}

func loadList(path string) (list, error) {
	l := list{names: map[string]bool{}, paths: map[string]bool{}}
	if path == "" {
		return l, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return l, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if filepath.IsAbs(line) {
			l.paths[normalizePath(line)] = true
		} else if strings.ContainsAny(line, `/\`) {
			return l, fmt.Errorf("entry %q must be a bare name or an absolute path", line)
		} else {
			l.names[normalizeName(line)] = true
		}
	}
	return l, scanner.Err()
}

// Enabled reports whether any list is configured.
func (p *Policy) Enabled() bool { return p != nil && (!p.allow.empty() || !p.deny.empty()) }

// Check decides whether argv0 may run. The denylist is consulted first; when
// an allowlist is configured the command must match it. A command matches an
// entry by its own name, by the name of the file it resolves to, or by either
// absolute path.
func (p *Policy) Check(argv0 string) error {
	if !p.Enabled() {
		return nil
	}
	names, paths := identities(argv0)
	if p.deny.matches(names, paths) {
		return fmt.Errorf("%w: %q is on the denylist", ErrDenied, argv0)
	}
	if !p.allow.empty() && !p.allow.matches(names, paths) {
		return fmt.Errorf("%w: %q is not on the allowlist", ErrDenied, argv0)
	}
	return nil
}

func (l list) matches(names, paths []string) bool {
	for _, n := range names {
		if l.names[n] {
			return true
		}
	}
	for _, p := range paths {
		if l.paths[p] {
			return true
		}
	}
	return false
}

func identities(argv0 string) (names, paths []string) {
	names = append(names, normalizeName(filepath.Base(argv0)))
	if filepath.IsAbs(argv0) {
		paths = append(paths, normalizePath(argv0))
	}
	if resolved, err := exec.LookPath(argv0); err == nil {
		if abs, err := filepath.Abs(resolved); err == nil {
			paths = append(paths, normalizePath(abs))
			names = append(names, normalizeName(filepath.Base(abs)))
			if real, err := filepath.EvalSymlinks(abs); err == nil {
				paths = append(paths, normalizePath(real))
				names = append(names, normalizeName(filepath.Base(real)))
			}
		}
	}
	return names, paths
}

func normalizeName(name string) string {
	if runtime.GOOS == "windows" {
		name = strings.ToLower(name)
		name = strings.TrimSuffix(name, ".exe")
	}
	return name
}

func normalizePath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}
