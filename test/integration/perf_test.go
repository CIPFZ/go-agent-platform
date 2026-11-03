//go:build linux

package integration

import (
	"os"
	"sort"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

func percentile(v []int64, p float64) int64 {
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(float64(len(s)-1)*p)]
}

// Spec 13.1 acceptance 2 and section 12 "performance": 1000 captures of the
// primary display, judged by elapsed_ms (grab, cursor overlay and encoding;
// no network). Run with VM_AGENT_PERF=1 on a display of the size to measure;
// JPEG must reach p50 < 60 ms and p99 < 150 ms at 1920x1080, PNG is
// reported only.
func TestCaptureLatency(t *testing.T) {
	requireX(t)
	if os.Getenv("VM_AGENT_PERF") != "1" {
		t.Skip("set VM_AGENT_PERF=1 to measure capture latency")
	}
	n := 1000
	a := startAgent(t)
	info, _ := a.client.ScreenInfo(ctx)
	b := info.Displays[0].Bounds
	w := startWindow(t, xwinSpec{title: "perf-pattern", x: b.X, y: b.Y, w: b.W, h: b.H, pattern: true})
	a.client.WindowActivate(ctx, &client.WindowSelector{ID: client.Ptr(w.id)})
	time.Sleep(500 * time.Millisecond)
	for _, tc := range []struct {
		format string
		cursor bool
		count  int
	}{{"jpeg", true, n}, {"jpeg", false, n / 4}, {"png", true, n / 4}} {
		var elapsed []int64
		var size int
		for i := 0; i < tc.count; i++ {
			var info client.ResponseInfo
			shot, err := a.client.ScreenCapture(ctx, &client.CaptureRequest{Format: client.Ptr(tc.format), Cursor: client.Ptr(tc.cursor)}, client.WithResponseInfo(&info))
			if err != nil {
				t.Fatal(err)
			}
			elapsed = append(elapsed, info.ElapsedMS)
			size = len(shot.Image)
		}
		p50, p99 := percentile(elapsed, 0.5), percentile(elapsed, 0.99)
		t.Logf("%dx%d %s cursor=%v: %d captures, p50 %d ms, p99 %d ms, %d KiB", b.W, b.H, tc.format, tc.cursor, tc.count, p50, p99, size/1024)
		if tc.format == "jpeg" && tc.cursor && b.W == 1920 && b.H == 1080 && (p50 >= 60 || p99 >= 150) {
			t.Errorf("1080p JPEG p50 %d ms / p99 %d ms exceeds the 60 / 150 ms gate", p50, p99)
		}
	}
}
