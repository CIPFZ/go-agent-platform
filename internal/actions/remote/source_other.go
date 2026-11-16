//go:build !windows

package remote

import "os/exec"

// The remote desktop is implemented for Windows only.
func platformCandidates() []candidate { return nil }

func platformDisplayActive() bool { return true }

func hideWindow(*exec.Cmd) {}
