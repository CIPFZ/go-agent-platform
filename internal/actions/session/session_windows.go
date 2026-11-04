//go:build windows

package session

import (
	"golang.org/x/sys/windows/registry"
	"os"

	"github.com/CIPFZ/go-agent-platform/internal/platform/win"
)

func detect() State {
	st := State{Type: "console"}
	st.Locked = !win.InputDesktopIsDefault()
	// A logged-in interactive session is a prerequisite for running at all;
	// the display is usable unless the input desktop has been switched away.
	st.DisplayReady = !st.Locked
	st.UACPrompt = uacPrompts()
	if protocol, err := win.ClientProtocolType(); err == nil && protocol == 2 {
		st.Type = "rdp"
	}
	return st
}

func initDPIAwareness() string { return win.InitDPIAwareness() }

// uacPrompts reports whether elevation prompts appear on the secure desktop:
// EnableLUA set and ConsentPromptBehaviorAdmin non-zero (spec section 10).
func uacPrompts() bool {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	lua, _, err := key.GetIntegerValue("EnableLUA")
	if err != nil || lua == 0 {
		return false
	}
	consent, _, err := key.GetIntegerValue("ConsentPromptBehaviorAdmin")
	if err != nil {
		return true // the Windows default (5) prompts
	}
	return consent != 0
}

// Wayland is false on Windows.
func Wayland() bool { return false }

// SetStateDir has nothing to configure on Windows.
func SetStateDir(string) {}

// PortalSetup is only meaningful in a Linux Wayland session.
func PortalSetup(string) int {
	os.Stderr.WriteString("vm-agent: VM_AGENT_PORTAL_SETUP applies to Linux Wayland sessions only\n")
	return 2
}
