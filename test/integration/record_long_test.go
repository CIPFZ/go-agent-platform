//go:build linux

package integration

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

// cpuTicks returns utime+stime of pid in clock ticks (USER_HZ = 100).
func cpuTicks(t *testing.T, pid int) int64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data)[strings.LastIndexByte(string(data), ')')+1:])
	u, _ := strconv.ParseInt(fields[11], 10, 64)
	s, _ := strconv.ParseInt(fields[12], 10, 64)
	return u + s
}

// Spec 13.2 acceptance 1: a 10 minute recording yields a playable MP4 and
// ffmpeg stays within 60 % of one core. VM_AGENT_RECORD_LONG=1 enables it;
// VM_AGENT_RECORD_SECONDS shortens it for trials.
func TestRecordTenMinutes(t *testing.T) {
	requireX(t)
	if os.Getenv("VM_AGENT_RECORD_LONG") != "1" {
		t.Skip("set VM_AGENT_RECORD_LONG=1 for the 10 minute recording check")
	}
	seconds := 600
	if v, err := strconv.Atoi(os.Getenv("VM_AGENT_RECORD_SECONDS")); err == nil {
		seconds = v
	}
	a := startAgent(t)
	requireRecord(t, a)
	info, _ := a.client.ScreenInfo(ctx)
	b := info.Displays[0].Bounds
	startWindow(t, xwinSpec{title: "record-long", animate: true, pattern: true, x: b.X, y: b.Y, w: b.W, h: b.H})
	start, err := a.client.RecordStart(ctx, &client.RecordStartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	procs, err := a.client.ProcessList(ctx, &client.ProcessListParams{Name: client.Ptr("ffmpeg")})
	if err != nil || len(procs.Processes) == 0 {
		t.Fatalf("ffmpeg not found: %v", err)
	}
	pid := procs.Processes[0].PID
	t0, c0 := time.Now(), cpuTicks(t, pid)
	time.Sleep(time.Duration(seconds) * time.Second)
	c1, t1 := cpuTicks(t, pid), time.Now()
	stop, err := a.client.RecordStop(ctx, &client.RecordStopRequest{SessionID: start.SessionID}, client.WithTimeout(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	cpu := float64(c1-c0) / 100 / t1.Sub(t0).Seconds() * 100
	video, err := a.client.RecordDownload(ctx, start.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "long.mp4")
	f, _ := os.Create(path)
	io.Copy(f, video.Body)
	f.Close()
	video.Close()
	dur, size := ffprobe(t, path)
	t.Logf("%d s at %dx%d fps 10: ffmpeg CPU %.1f%% of one core, file %d bytes, ffprobe duration %.1f s, %s",
		seconds, b.W, b.H, cpu, stop.Size, dur, size)
	if cpu > 60 {
		t.Errorf("ffmpeg used %.1f%% of a core, over the 60%% limit", cpu)
	}
	if dur < float64(seconds)*0.98 {
		t.Errorf("playable duration %.1f s, want about %d s", dur, seconds)
	}
}
