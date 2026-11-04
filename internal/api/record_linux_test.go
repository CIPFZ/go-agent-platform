//go:build linux

package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/session"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

const fakeFFmpeg = `#!/bin/sh
out=""; for a in "$@"; do out="$a"; done
printf 'HEADER' > "$out"
while read line; do
  if [ "$line" = q ]; then printf 'TRAILER' >> "$out"; exit 0; fi
done
`

func recordEnv(t *testing.T) *testEnv {
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	os.WriteFile(bin, []byte(fakeFFmpeg), 0o755)
	return newEnv(t, func(d *Deps) {
		d.FFmpeg = bin
		d.RecordGrabArgs = func(int, protocol.Rect) ([]string, error) { return []string{"-f", "fake"}, nil }
	})
}

func TestRecordLifecycle(t *testing.T) {
	e := recordEnv(t)
	var caps protocol.CapabilitiesResult
	e.call("GET", "/v1/capabilities", nil).into(t, &caps)
	if caps.Actions[len(caps.Actions)-1] != "record" {
		t.Fatalf("actions = %v", caps.Actions)
	}
	var started struct {
		SessionID string    `json:"session_id"`
		StartedAt time.Time `json:"started_at"`
	}
	e.call("POST", "/v1/record/start", map[string]any{"region": map[string]int{"x": 1, "y": 1, "w": 101, "h": 51}}).into(t, &started)
	if started.SessionID == "" || started.StartedAt.IsZero() {
		t.Fatalf("start = %+v", started)
	}
	e.call("POST", "/v1/record/start", `{}`).code(t, protocol.CodeConflict)
	e.call("GET", "/v1/record/"+started.SessionID, nil).code(t, protocol.CodeConflict)
	var list struct {
		Sessions []map[string]any `json:"sessions"`
	}
	e.call("GET", "/v1/record/list", nil).into(t, &list)
	if len(list.Sessions) != 1 || list.Sessions[0]["status"] != "recording" {
		t.Fatalf("list = %v", list)
	}
	var stopped map[string]any
	e.call("POST", "/v1/record/stop", map[string]any{"session_id": started.SessionID}).into(t, &stopped)
	if stopped["size"] != float64(13) || len(stopped["sha256"].(string)) != 64 {
		t.Fatalf("stop = %v", stopped)
	}
	r := e.call("GET", "/v1/record/"+started.SessionID, nil)
	if r.Status != 200 || string(r.Body) != "HEADERTRAILER" || r.Header.Get("Content-Type") != "video/mp4" || r.Header.Get(protocol.HeaderResultSHA256) != stopped["sha256"] {
		t.Fatalf("download = %d %q %v", r.Status, r.Body, r.Header)
	}
	e.call("DELETE", "/v1/record/"+started.SessionID, nil).into(t, &map[string]any{})
	e.call("GET", "/v1/record/"+started.SessionID, nil).code(t, protocol.CodeNotFound)
	e.call("POST", "/v1/record/stop", map[string]any{"session_id": "nope"}).code(t, protocol.CodeNotFound)
	for _, bad := range []string{`{"fps":0}`, `{"fps":61}`, `{"crf":52}`, `{"preset":"warp"}`, `{"display":5}`} {
		e.call("POST", "/v1/record/start", bad).code(t, protocol.CodeInvalidRequest)
	}
}

func TestRecordUnavailableWithoutFFmpeg(t *testing.T) {
	e := newEnv(t)
	var caps protocol.CapabilitiesResult
	e.call("GET", "/v1/capabilities", nil).into(t, &caps)
	for _, a := range caps.Actions {
		if a == "record" {
			t.Fatal("record advertised without ffmpeg")
		}
	}
	e.call("POST", "/v1/record/start", `{}`).code(t, protocol.CodeUnsupported)
}

func TestShutdownFinishesRecording(t *testing.T) {
	e := recordEnv(t)
	var started struct {
		SessionID string `json:"session_id"`
	}
	e.call("POST", "/v1/record/start", `{}`).into(t, &started)
	e.api.Shutdown()
	var list struct {
		Sessions []map[string]any `json:"sessions"`
	}
	e.call("GET", "/v1/record/list", nil).into(t, &list)
	if list.Sessions[0]["status"] != "finished" || list.Sessions[0]["size"] != float64(13) {
		t.Fatalf("after shutdown = %v", list.Sessions)
	}
	e.setSession(session.State{})
	e.call("POST", "/v1/record/start", `{}`).code(t, protocol.CodeNoDisplay)
}
