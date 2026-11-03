// Package session detects the desktop-session preconditions reported by
// /v1/probe (spec section 4.2): display readiness, lock state, session type,
// UAC secure-desktop prompts and DPI awareness.
package session

// State is a snapshot of the session preconditions. It is detected live on
// every call.
type State struct {
	DisplayReady bool
	Locked       bool
	Type         string // "console" or "rdp"
	UACPrompt    bool
}

// Detect returns the current session state.
func Detect() State { return detect() }

// InitDPIAwareness declares the process DPI awareness (Windows) and returns
// the level in effect, e.g. "per_monitor_v2". Call once at startup.
func InitDPIAwareness() string { return initDPIAwareness() }
