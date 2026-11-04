//go:build !windows

package process

import "os/exec"

// HideConsole is a no-op outside Windows.
func HideConsole(*exec.Cmd) {}
