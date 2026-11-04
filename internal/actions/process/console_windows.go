//go:build windows

package process

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// HideConsole keeps a console program started by cmd from opening a console
// window. The Windows build is a GUI-subsystem program (-H=windowsgui) with
// no console of its own, so without CREATE_NO_WINDOW every console child
// (powershell, cmd, ffmpeg) would get a new visible console window on the
// automated desktop, covering it and taking the focus. GUI programs ignore
// the flag.
func HideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
