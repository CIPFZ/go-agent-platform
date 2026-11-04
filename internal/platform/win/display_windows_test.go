//go:build windows

package win

import "testing"

// Windows 10 1703 and later accept Per-Monitor V2 (spec 4.1), and the level
// must be read back from the thread's awareness context, not guessed from
// IsProcessDPIAware (which is also true for per-monitor awareness).
func TestInitDPIAwarenessReportsPerMonitorV2(t *testing.T) {
	if procSetProcessDpiAwarenessContext.Find() != nil {
		t.Skip("Windows older than 10 1703")
	}
	if got := InitDPIAwareness(); got != "per_monitor_v2" {
		t.Fatalf("DPI awareness = %q", got)
	}
}

// The scale comes from GetDpiForMonitor and matches the system DPI for the
// primary display of a Per-Monitor V2 process.
func TestMonitorScaleFromDPI(t *testing.T) {
	InitDPIAwareness()
	mons, err := Monitors()
	if err != nil || len(mons) == 0 {
		t.Skipf("no monitors: %v", err)
	}
	getDpiForSystem := user32.NewProc("GetDpiForSystem")
	if getDpiForSystem.Find() != nil {
		t.Skip("no GetDpiForSystem")
	}
	dpi, _ := Call(getDpiForSystem)
	if want := float64(dpi) / 96; mons[0].Scale != want {
		t.Fatalf("primary scale %v, system DPI %d", mons[0].Scale, dpi)
	}
}
