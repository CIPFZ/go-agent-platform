//go:build !linux && !windows

package session

import "os"

func detect() State { return State{Type: "console"} }

func initDPIAwareness() string { return "unaware" }

// Wayland is false on this platform.
func Wayland() bool { return false }

// SetStateDir has nothing to configure on this platform.
func SetStateDir(string) {}

// PortalSetup is only meaningful in a Linux Wayland session.
func PortalSetup(string) int {
	os.Stderr.WriteString("vm-agent: VM_AGENT_PORTAL_SETUP applies to Linux Wayland sessions only\n")
	return 2
}
