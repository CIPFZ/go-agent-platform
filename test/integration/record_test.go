//go:build linux

package integration

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

func requireRecord(t *testing.T, a *agent) {
	t.Helper()
	caps, err := a.client.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, act := range caps.Actions {
		if act == "record" {
			return
		}
	}
	t.Skip("record is not available (ffmpeg with libx264 and x11grab missing)")
}

// ffprobe returns the duration in seconds and the video size.
func ffprobe(t *testing.T, path string) (float64, string) {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height:format=duration", "-of", "default=nw=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var dur float64
	var w, h string
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "duration":
			dur, _ = strconv.ParseFloat(v, 64)
		case "width":
			w = v
		case "height":
			h = v
		}
	}
	return dur, w + "x" + h
}

func TestRecordScreenToPlayableMP4(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	requireRecord(t, a)
	startWindow(t, xwinSpec{title: "record-anim", animate: true, x: 0, y: 0, w: 800, h: 600})
	start, err := a.client.RecordStart(ctx, &client.RecordStartRequest{Region: &client.Rect{X: 0, Y: 0, W: 641, H: 481}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.client.RecordStart(ctx, &client.RecordStartRequest{}); apiErr(t, err).Code != "conflict" {
		t.Fatal("second recording started")
	}
	time.Sleep(3 * time.Second)
	stop, err := a.client.RecordStop(ctx, &client.RecordStopRequest{SessionID: start.SessionID})
	if err != nil || stop.Size < 1000 || stop.DurationMS < 2500 {
		t.Fatalf("stop = %+v %v", stop, err)
	}
	video, err := a.client.RecordDownload(ctx, start.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "out.mp4")
	f, _ := os.Create(path)
	n, _ := io.Copy(f, video.Body)
	f.Close()
	video.Close()
	if n != stop.Size || video.Header.Get("X-Result-Sha256") != stop.SHA256 {
		t.Fatalf("downloaded %d of %d bytes", n, stop.Size)
	}
	if _, err := exec.LookPath("ffprobe"); err == nil {
		dur, size := ffprobe(t, path)
		if dur < 2 || size != "640x480" {
			t.Fatalf("ffprobe duration %.1f size %s", dur, size)
		}
		t.Logf("recorded %.1f s at %s, %d bytes", dur, size, n)
	}
	if err := a.client.RecordDelete(ctx, start.SessionID); err != nil {
		t.Fatal(err)
	}
}

// A recording in progress is finished cleanly when vm-agent stops (spec
// 4.4 step 3) and is listed after the restart.
func TestRecordingFinishedOnShutdown(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	requireRecord(t, a)
	start, err := a.client.RecordStart(ctx, &client.RecordStartRequest{Region: &client.Rect{W: 320, H: 240}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	a.stop()
	a.start()
	list, err := a.client.RecordList(ctx)
	if err != nil || len(list.Sessions) != 1 || list.Sessions[0].SessionID != start.SessionID || list.Sessions[0].Status != "finished" || list.Sessions[0].Size == 0 {
		t.Fatalf("after restart = %+v %v", list, err)
	}
}
