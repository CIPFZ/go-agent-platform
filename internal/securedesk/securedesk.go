// Package securedesk gives the web remote desktop access to the lock screen
// and other secure desktops (docs/remote-desktop-design.md 5.14).
//
// vm-agent runs as the desktop user and Windows lets no user process see or
// drive those desktops. Two more processes of the same executable do it:
//
//   - "vm-agent -desktop-service" is a Windows service (SYSTEM, session 0).
//     It only keeps a helper running in the console session.
//   - "vm-agent -desktop-helper" runs as SYSTEM inside that session, on the
//     Winlogon desktop. It serves a named pipe with two operations: apply an
//     input message, and capture the desktop with ffmpeg as RTP to a
//     loopback port. It runs no command a client gives it; the ffmpeg path
//     comes from the service's own command line.
//
// vm-agent talks to the helper only while the desktop is locked.
package securedesk

import "fmt"

// ServiceName is the Windows service name.
const ServiceName = "vm-agent-desktop"

// PipePath is the helper's pipe for a session.
func PipePath(session uint32) string { return fmt.Sprintf(`\\.\pipe\vm-agent-desktop-%d`, session) }

// request opens a connection to the helper. Op "input" is followed by one
// input message of the page per line; op "video" starts a capture that lasts
// as long as the connection, and the helper writes one reply line if it
// ends by itself.
type request struct {
	Op    string `json:"op"`
	FPS   int    `json:"fps,omitempty"`
	Kbps  int    `json:"kbps,omitempty"`
	Port  int    `json:"port,omitempty"`
	W     int    `json:"w,omitempty"`
	H     int    `json:"h,omitempty"`
	Error string `json:"error,omitempty"`
}
