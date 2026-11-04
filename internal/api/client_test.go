package api

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/window"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

// The OpenAPI document (through the generated client) and the route table
// must describe the same endpoints.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	e := newEnv(t)
	var routes []string
	for _, r := range e.api.Routes() {
		routes = append(routes, r.Method+" "+r.Path)
	}
	var documented []string
	for _, op := range client.Operations {
		documented = append(documented, op.Method+" "+op.Path)
	}
	sort.Strings(routes)
	sort.Strings(documented)
	if len(routes) != len(documented) {
		t.Fatalf("routes %d, documented %d:\n%v\n%v", len(routes), len(documented), routes, documented)
	}
	for i := range routes {
		if routes[i] != documented[i] {
			t.Fatalf("route %q vs documented %q", routes[i], documented[i])
		}
	}
}

func TestGeneratedClientAgainstServer(t *testing.T) {
	fw := &fakeWindows{list: []window.Window{{ID: "0x9", Title: "Doc", Class: "Ed", PID: 3, Visible: true}}}
	e := newEnv(t, func(d *Deps) { d.Window = fw })
	c := client.New(e.http.URL)
	ctx := context.Background()

	var info client.ResponseInfo
	probe, err := c.Probe(ctx, client.WithResponseInfo(&info), client.WithRequestID("probe-request-0001"))
	if err != nil || probe.APIVersion != "v1" || info.RequestID != "probe-request-0001" || info.AgentVersion == "" {
		t.Fatalf("probe = %+v %+v %v", probe, info, err)
	}
	if caps, err := c.Capabilities(ctx); err != nil || caps.Limits.BatchMaxActions != 200 {
		t.Fatalf("caps = %+v %v", caps, err)
	}
	shot, err := c.ScreenCapture(ctx, &client.CaptureRequest{Format: client.Ptr("png"), Region: &client.Rect{W: 4, H: 4}})
	if err != nil || shot.Width != 4 || len(shot.Image) == 0 {
		t.Fatalf("capture = %v", err)
	}
	stream, err := c.ScreenCaptureStream(ctx, &client.CaptureRequest{})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(stream.Body)
	stream.Close()
	if stream.ContentType != "image/jpeg" || int64(len(data)) != stream.Size {
		t.Fatalf("stream %s %d/%d", stream.ContentType, len(data), stream.Size)
	}
	if _, err := c.ScreenInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ScreenCursor(ctx); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"move":  func() error { return c.MouseMove(ctx, &client.MouseMoveRequest{X: 1, Y: 2}) },
		"click": func() error { return c.MouseClick(ctx, &client.MouseClickRequest{X: client.Ptr(0), Y: client.Ptr(0)}) },
		"down":  func() error { return c.MouseDown(ctx, &client.MouseButtonRequest{}) },
		"up":    func() error { return c.MouseUp(ctx, &client.MouseButtonRequest{Button: client.Ptr("left")}) },
		"drag": func() error {
			return c.MouseDrag(ctx, &client.MouseDragRequest{From: client.Point{}, To: client.Point{X: 3}, DurationMS: client.Ptr(0)})
		},
		"scroll": func() error { return c.MouseScroll(ctx, &client.MouseScrollRequest{DY: client.Ptr(-1)}) },
		"type":   func() error { return c.KeyType(ctx, &client.KeyTypeRequest{Text: "hi"}) },
		"press":  func() error { return c.KeyPress(ctx, &client.KeyRequest{Key: "enter"}) },
		"kdown":  func() error { return c.KeyDown(ctx, &client.KeyRequest{Key: "shift"}) },
		"kup":    func() error { return c.KeyUp(ctx, &client.KeyRequest{Key: "shift"}) },
		"hotkey": func() error { return c.KeyHotkey(ctx, &client.HotkeyRequest{Keys: []string{"ctrl", "s"}}) },
	} {
		if err := call(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	batch, err := c.InputBatch(ctx, &client.BatchRequest{Actions: []client.BatchAction{
		{Type: "mouse.move", X: client.Ptr(5), Y: client.Ptr(5)},
		{Type: "wait", MS: client.Ptr(1)},
		{Type: "key.hotkey", Keys: []string{"alt", "tab"}, DelayAfterMS: client.Ptr(1)},
	}})
	if err != nil || batch.Executed != 3 {
		t.Fatalf("batch = %+v %v", batch, err)
	}
	wl, err := c.WindowList(ctx, &client.WindowListParams{VisibleOnly: client.Ptr(false)})
	if err != nil || len(wl.Windows) != 1 || wl.Windows[0].Rect != nil {
		t.Fatalf("windows = %+v %v", wl, err)
	}
	if w, err := c.WindowActivate(ctx, &client.WindowSelector{Title: client.Ptr("Doc")}); err != nil || !w.Active {
		t.Fatalf("activate = %+v %v", w, err)
	}
	if w, err := c.WindowActive(ctx); err != nil || w.ID != "0x9" {
		t.Fatalf("active = %+v %v", w, err)
	}
	if _, err := c.WindowClose(ctx, &client.WindowSelector{ID: client.Ptr("0x9")}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if w, err := c.FileWrite(ctx, &client.FileWriteRequest{Path: p, Content: []byte("data")}); err != nil || w.Size != 4 {
		t.Fatalf("write = %+v %v", w, err)
	}
	if r, err := c.FileRead(ctx, &client.FileReadRequest{Path: p}); err != nil || string(r.Content) != "data" {
		t.Fatalf("read = %+v %v", r, err)
	}
	rs, err := c.FileReadStream(ctx, &client.FileReadRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rs.Body)
	rs.Close()
	if string(body) != "data" || rs.Header.Get("X-File-Mtime") == "" {
		t.Fatalf("read stream = %q", body)
	}
	if st, err := c.FileStat(ctx, &client.FilePathRequest{Path: p}); err != nil || !st.Exists || *st.Type != "file" {
		t.Fatalf("stat = %+v %v", st, err)
	}
	if err := c.FileMkdir(ctx, &client.FilePathRequest{Path: filepath.Join(dir, "sub")}); err != nil {
		t.Fatal(err)
	}
	if l, err := c.FileList(ctx, &client.FileListRequest{Path: dir}); err != nil || len(l.Entries) != 2 {
		t.Fatalf("list = %+v %v", l, err)
	}
	if k, err := c.FileKnownDirs(ctx); err != nil || k.Home == "" {
		t.Fatalf("known = %+v %v", k, err)
	}
	if _, err := c.ProcessOpen(ctx, &client.OpenRequest{URL: client.Ptr("gopher://x")}); err == nil {
		t.Fatal("open accepted gopher")
	}

	// Typed errors, including dedup replays answered with HTTP 200.
	_, err = c.FileRead(ctx, &client.FileReadRequest{Path: filepath.Join(dir, "missing")})
	var apiErr *client.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" || apiErr.HTTPStatus != 404 {
		t.Fatalf("err = %#v", err)
	}
	req := &client.FileWriteRequest{Path: p, Content: []byte("x")}
	_, err1 := c.FileWrite(ctx, req, client.WithRequestID("write-dup-0000001"))
	var replay client.ResponseInfo
	_, err2 := c.FileWrite(ctx, req, client.WithRequestID("write-dup-0000001"), client.WithResponseInfo(&replay))
	if !errors.As(err1, &apiErr) || apiErr.Code != "conflict" || !errors.As(err2, &apiErr) || apiErr.HTTPStatus != 200 || !replay.DedupHit {
		t.Fatalf("dedup replay: %v / %v / %+v", err1, err2, replay)
	}
	_, err = c.Exec(ctx, &client.ExecRequest{Argv: []string{"no-such-program-xyz"}}, client.WithTimeout(2e9))
	if !errors.As(err, &apiErr) || apiErr.Code != string(protocol.CodeNotFound) {
		t.Fatalf("missing program = %v", err)
	}
	// The test binary itself runs anywhere: with no tests selected it prints PASS.
	job, err := c.Exec(ctx, &client.ExecRequest{Argv: []string{os.Args[0], "-test.run=^$"}, Detach: client.Ptr(true)})
	if err != nil || job.JobID == nil || job.ExitCode != nil {
		t.Fatalf("detached = %+v %v", job, err)
	}
	for {
		j, err := c.ExecJob(ctx, *job.JobID, &client.ExecJobParams{TailBytes: client.Ptr(int64(0))})
		if err != nil {
			t.Fatal(err)
		}
		if j.Status != "running" {
			if j.Status != "exited" || !strings.Contains(j.Stdout, "PASS") {
				t.Fatalf("job = %+v", j)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if jobs, err := c.ExecJobs(ctx); err != nil || len(jobs.Jobs) != 1 {
		t.Fatalf("jobs = %+v %v", jobs, err)
	}
	if _, err := c.ExecJobCancel(ctx, *job.JobID); err != nil {
		t.Fatal(err)
	}
	if err := c.ExecJobDelete(ctx, *job.JobID); err != nil {
		t.Fatal(err)
	}
	_, err = c.Exec(ctx, &client.ExecRequest{Argv: []string{"no-such-program-xyz"}}, client.WithTimeout(2e9))
	if !errors.As(err, &apiErr) || apiErr.Code != string(protocol.CodeNotFound) {
		t.Fatalf("missing program = %v", err)
	}
	_ = os.Remove(p)
}
