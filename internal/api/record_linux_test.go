//go:build linux

package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	miniocreds "github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/CIPFZ/go-agent-platform/internal/actions/session"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/upload"
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

// TestRecordUploadToObjectStorage runs when VM_AGENT_S3_ENDPOINT points at
// an S3-compatible server (VersityGW in CI, MinIO works the same).
func TestRecordUploadToObjectStorage(t *testing.T) {
	endpoint := os.Getenv("VM_AGENT_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set VM_AGENT_S3_ENDPOINT, VM_AGENT_S3_ACCESS_KEY and VM_AGENT_S3_SECRET_KEY")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a"), []byte(os.Getenv("VM_AGENT_S3_ACCESS_KEY")), 0o600)
	os.WriteFile(filepath.Join(dir, "s"), []byte(os.Getenv("VM_AGENT_S3_SECRET_KEY")), 0o600)
	admin, err := minio.New(endpoint, &minio.Options{Creds: miniocreds.NewStaticV4(os.Getenv("VM_AGENT_S3_ACCESS_KEY"), os.Getenv("VM_AGENT_S3_SECRET_KEY"), "")})
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := admin.BucketExists(context.Background(), "vm-agent-api"); err != nil {
		t.Fatal(err)
	} else if !exists {
		if err := admin.MakeBucket(context.Background(), "vm-agent-api", minio.MakeBucketOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	up, err := upload.New(upload.Config{Endpoint: endpoint, Bucket: "vm-agent-api", Prefix: "api", AccessKeyFile: filepath.Join(dir, "a"), SecretKeyFile: filepath.Join(dir, "s"), Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	os.WriteFile(bin, []byte(fakeFFmpeg), 0o755)
	e := newEnv(t, func(d *Deps) {
		d.FFmpeg, d.Uploader = bin, up
		d.RecordGrabArgs = func(int, protocol.Rect) ([]string, error) { return []string{"-f", "fake"}, nil }
	})
	var caps protocol.CapabilitiesResult
	e.call("GET", "/v1/capabilities", nil).into(t, &caps)
	if caps.Actions[len(caps.Actions)-1] != "record_upload" {
		t.Fatalf("actions = %v", caps.Actions)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	e.call("POST", "/v1/record/start", `{}`).into(t, &started)
	e.call("POST", "/v1/record/"+started.SessionID+"/upload", `{}`).code(t, protocol.CodeConflict)
	e.call("POST", "/v1/record/stop", map[string]any{"session_id": started.SessionID}).into(t, &map[string]any{})
	var res map[string]any
	e.call("POST", "/v1/record/"+started.SessionID+"/upload", `{}`).into(t, &res)
	if res["key"] != "api/"+started.SessionID+".mp4" || res["size"] != float64(13) || res["bucket"] != "vm-agent-api" {
		t.Fatalf("upload = %v", res)
	}
	e.call("POST", "/v1/record/"+started.SessionID+"/upload", `{"key":"custom/name.mp4"}`).into(t, &res)
	if res["key"] != "api/custom/name.mp4" {
		t.Fatalf("custom key = %v", res)
	}
	e.call("POST", "/v1/record/"+started.SessionID+"/upload", `{"key":"../escape"}`).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/record/nope/upload", `{}`).code(t, protocol.CodeNotFound)
}

func TestRecordUploadUnsupportedWithoutStorage(t *testing.T) {
	e := recordEnv(t)
	e.call("POST", "/v1/record/x/upload", `{}`).code(t, protocol.CodeUnsupported)
}
