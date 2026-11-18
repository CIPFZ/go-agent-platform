package remote

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The marker records that this vm-agent connected a virtual monitor. It
// outlives a crash, which a flag in memory would not.
func markerPath(dir string) string { return filepath.Join(dir, "remote-virtual-display") }

func markerExists(dir string) bool {
	_, err := os.Stat(markerPath(dir))
	return err == nil
}

func writeMarker(dir string) {
	if dir != "" {
		_ = os.WriteFile(markerPath(dir), []byte("plugged\n"), 0o600)
	}
}

func removeMarker(dir string) {
	if dir != "" {
		_ = os.Remove(markerPath(dir))
	}
}

// displayRecord is the size and scale of the real display, noted while one
// was active. A virtual monitor is given the same, so the desktop does not
// change when it takes over. What the desktop reports while no display is
// active cannot be relied on for this: after the driver was reinstalled on
// the test laptop it reported 200 % where the panel uses 150 %.
type displayRecord struct {
	W     int     `json:"w"`
	H     int     `json:"h"`
	Scale float64 `json:"scale"`
}

func recordPath(dir string) string { return filepath.Join(dir, "remote-display.json") }

func loadDisplayRecord(dir string) (displayRecord, bool) {
	var r displayRecord
	data, err := os.ReadFile(recordPath(dir))
	if dir == "" || err != nil || json.Unmarshal(data, &r) != nil || r.W <= 0 || r.H <= 0 || r.Scale <= 0 {
		return displayRecord{}, false
	}
	return r, true
}

func saveDisplayRecord(dir string, r displayRecord) {
	if dir == "" {
		return
	}
	if old, ok := loadDisplayRecord(dir); ok && old == r {
		return
	}
	if data, err := json.Marshal(r); err == nil {
		_ = os.WriteFile(recordPath(dir), append(data, '\n'), 0o600)
	}
}
