//go:build linux

package record

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// fakeFFmpeg writes a header to the output (the last argument), then
// appends a trailer and exits when "q" arrives on stdin. FAKE_MODE=fail
// exits at once with an error; FAKE_MODE=ignore never exits by itself.
const fakeFFmpeg = `#!/bin/sh
out=""; for a in "$@"; do out="$a"; done
if [ "$FAKE_MODE" = fail ]; then echo "Cannot open display :0" >&2; exit 1; fi
printf 'HEADER' > "$out"
if [ "$FAKE_MODE" = ignore ]; then trap '' TERM; while :; do sleep 1; done; fi
while read line; do
  if [ "$line" = q ]; then printf 'TRAILER' >> "$out"; exit 0; fi
done
`

func fakeRecorder(t *testing.T, quota int64, retention time.Duration) *Recorder {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bin, []byte(fakeFFmpeg), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := Open(t.TempDir(), bin, quota, retention, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.grabArgs = func(int, protocol.Rect) ([]string, error) { return []string{"-f", "fake"}, nil }
	r.stopGrace = 300 * time.Millisecond
	return r
}

func opts() Options {
	return Options{FPS: 10, Rect: protocol.Rect{W: 640, H: 480}, Preset: "ultrafast", CRF: 28}
}

func TestStartStopDownloadDelete(t *testing.T) {
	r := fakeRecorder(t, 1<<30, time.Hour)
	s, err := r.Start(context.Background(), opts())
	if err != nil || s.Status != StatusRecording {
		t.Fatalf("start = %+v %v", s, err)
	}
	if _, err := r.Start(context.Background(), opts()); !errors.Is(err, ErrActive) {
		t.Fatalf("second start = %v", err)
	}
	if _, _, err := r.OpenVideo(s.ID); !errors.Is(err, ErrStillActive) {
		t.Fatalf("download while recording = %v", err)
	}
	if err := r.Delete(s.ID); !errors.Is(err, ErrStillActive) {
		t.Fatalf("delete while recording = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	done, err := r.Stop(s.ID)
	sum := sha256.Sum256([]byte("HEADERTRAILER"))
	if err != nil || done.Status != StatusFinished || done.Size != 13 || done.SHA256 != hex.EncodeToString(sum[:]) || done.DurationMS < 50 || done.FinishedAt == nil {
		t.Fatalf("stop = %+v %v", done, err)
	}
	if again, err := r.Stop(s.ID); err != nil || again.Size != 13 {
		t.Fatalf("second stop = %+v %v", again, err)
	}
	f, meta, err := r.OpenVideo(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "HEADERTRAILER" || meta.Size != 13 {
		t.Fatalf("video = %q", data)
	}
	if list := r.List(); len(list) != 1 || list[0].ID != s.ID {
		t.Fatalf("list = %+v", list)
	}
	if err := r.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Stop(s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stop after delete = %v", err)
	}
	if entries, _ := os.ReadDir(r.dir); len(entries) != 0 {
		t.Fatalf("files left: %v", entries)
	}
	// The slot is free again.
	s2, err := r.Start(context.Background(), opts())
	if err != nil {
		t.Fatal(err)
	}
	r.Stop(s2.ID)
}

func TestStartFailureReportsFFmpegError(t *testing.T) {
	t.Setenv("FAKE_MODE", "fail")
	r := fakeRecorder(t, 1<<30, time.Hour)
	_, err := r.Start(context.Background(), opts())
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeIOFailed || !strings.Contains(perr.Message, "Cannot open display") {
		t.Fatalf("err = %v", err)
	}
	if r.Active() != nil || len(r.List()) != 0 {
		t.Fatal("failed start left state behind")
	}
}

func TestStopKillsFFmpegIgnoringQuit(t *testing.T) {
	t.Setenv("FAKE_MODE", "ignore")
	r := fakeRecorder(t, 1<<30, time.Hour)
	s, err := r.Start(context.Background(), opts())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	done, err := r.Stop(s.ID)
	if err != nil || done.Size != 6 || time.Since(start) < 300*time.Millisecond || time.Since(start) > 3*time.Second {
		t.Fatalf("stop = %+v %v after %s", done, err, time.Since(start))
	}
}

func TestShutdownStopsActiveRecording(t *testing.T) {
	r := fakeRecorder(t, 1<<30, time.Hour)
	s, _ := r.Start(context.Background(), opts())
	r.Shutdown()
	list := r.List()
	if len(list) != 1 || list[0].Status != StatusFinished || list[0].Size != 13 {
		t.Fatalf("after shutdown = %+v", list)
	}
	if _, err := r.Start(context.Background(), opts()); err == nil {
		t.Fatal("start accepted after shutdown")
	}
	_ = s
}

func writeFinished(t *testing.T, dir, id string, size int, finished time.Time) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, id+".mp4"), make([]byte, size), 0o600)
	s := &Session{ID: id, Status: StatusFinished, StartedAt: finished.Add(-time.Minute), FinishedAt: &finished, Size: int64(size)}
	if err := writeSession(filepath.Join(dir, id+".json"), s); err != nil {
		t.Fatal(err)
	}
}

// Spec 13.2 acceptance 2: over quota the oldest go first; past retention
// finished recordings go regardless of size.
func TestQuotaAndRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeFinished(t, dir, "rec-a", 400, now.Add(-3*time.Hour))
	writeFinished(t, dir, "rec-b", 400, now.Add(-2*time.Hour))
	writeFinished(t, dir, "rec-c", 400, now.Add(-1*time.Hour))
	r, err := Open(dir, "", 1000, 24*time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ids := func() []string {
		var out []string
		for _, s := range r.List() {
			out = append(out, s.ID)
		}
		return out
	}
	if got := strings.Join(ids(), ","); got != "rec-c,rec-b" {
		t.Fatalf("after quota = %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "rec-a.mp4")); !os.IsNotExist(err) {
		t.Fatal("oldest recording file kept")
	}
	later := now.Add(22*time.Hour + 30*time.Minute) // rec-b is now 24.5 h old
	r.now = func() time.Time { return later }
	if removed := r.Prune(); len(removed) != 1 || removed[0] != "rec-b" {
		t.Fatalf("retention removed %v", removed)
	}
	if got := strings.Join(ids(), ","); got != "rec-c" {
		t.Fatalf("after retention = %s", got)
	}
}

func TestCrashedRecordingIsFinalised(t *testing.T) {
	dir := t.TempDir()
	started := time.Now().UTC().Add(-time.Minute)
	os.WriteFile(filepath.Join(dir, "rec-x.mp4"), []byte("partial"), 0o600)
	writeSession(filepath.Join(dir, "rec-x.json"), &Session{ID: "rec-x", Status: StatusRecording, StartedAt: started})
	r, err := Open(dir, "", 1<<30, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := r.List()[0]
	if s.Status != StatusFailed || s.Size != 7 || s.FinishedAt == nil || s.DurationMS <= 0 || s.Error == "" {
		t.Fatalf("recovered = %+v", s)
	}
}

// With a real ffmpeg (libx264 and lavfi), the output is a playable MP4 even
// though the input is synthetic.
func TestRealFFmpegProducesPlayableMP4(t *testing.T) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if out, _ := exec.Command(path, "-hide_banner", "-encoders").Output(); !strings.Contains(string(out), "libx264") {
		t.Skip("ffmpeg lacks libx264")
	}
	r, err := Open(t.TempDir(), path, 1<<30, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.grabArgs = func(fps int, rect protocol.Rect) ([]string, error) {
		return []string{"-re", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=10"}, nil
	}
	s, err := r.Start(context.Background(), opts())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	done, err := r.Stop(s.ID)
	if err != nil || done.Size < 1000 {
		t.Fatalf("stop = %+v %v", done, err)
	}
	if probe, err := exec.LookPath("ffprobe"); err == nil {
		out, err := exec.Command(probe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", filepath.Join(r.dir, s.ID+".mp4")).Output()
		if err != nil || strings.TrimSpace(string(out)) == "" {
			t.Fatalf("ffprobe: %q %v", out, err)
		}
		t.Logf("duration %s s, %d bytes", strings.TrimSpace(string(out)), done.Size)
	}
}
