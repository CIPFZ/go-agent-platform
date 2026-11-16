//go:build windows

package win

import "time"

var procDwmFlush = dwmapi.NewProc("DwmFlush")

// displayActiveInterval separates a compositor that follows a live display
// from one that idles: DwmFlush returns once per refresh of an active display
// (8 ms at 120 Hz, 17 ms at 60 Hz) and about every 45 ms when no display is
// active, as on a laptop with its lid closed.
const displayActiveInterval = 30 * time.Millisecond

// DisplayActive reports whether the desktop compositor is presenting to an
// active display. Desktop Duplication delivers frames only in that state.
func DisplayActive() bool {
	if !HResult(procDwmFlush) {
		return false
	}
	start := time.Now()
	const rounds = 3
	for i := 0; i < rounds; i++ {
		if !HResult(procDwmFlush) {
			return false
		}
	}
	return time.Since(start)/rounds < displayActiveInterval
}
