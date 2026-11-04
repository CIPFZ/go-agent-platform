//go:build linux

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

var ctx = context.Background()

func apiErr(t *testing.T, err error) *client.Error {
	t.Helper()
	var e *client.Error
	if !errors.As(err, &e) {
		t.Fatalf("expected a vm-agent error, got %v", err)
	}
	return e
}

func TestProbeAndCapabilitiesOnX11(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	p, err := a.client.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Display.Ready || p.Display.Count < 1 || p.Display.Primary == nil || p.SessionType != "console" || p.Platform != "linux" {
		t.Fatalf("probe = %+v", p)
	}
	caps, err := a.client.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(caps.Actions, ",") != "screen,input,window,exec,process,file,clipboard" {
		t.Fatalf("actions = %v", caps.Actions)
	}
}

func TestCaptureSeesWindowContent(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	w := startWindow(t, xwinSpec{title: "capture-target", color: "ff0000", x: 200, y: 150, w: 300, h: 200})
	win, err := a.client.WindowActivate(ctx, &client.WindowSelector{ID: client.Ptr(w.id)})
	if err != nil {
		t.Fatal(err)
	}
	cr := win.ClientRect
	if cr == nil {
		t.Fatal("no client rect")
	}
	shot, err := a.client.ScreenCapture(ctx, &client.CaptureRequest{Format: client.Ptr("png"), Region: &client.Rect{X: cr.X + 10, Y: cr.Y + 10, W: 50, H: 40}})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(shot.Image))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(25, 20).RGBA()
	if r>>8 != 0xff || g>>8 != 0 || b>>8 != 0 || shot.Width != 50 || shot.Height != 40 {
		t.Fatalf("pixel = %x %x %x, size %dx%d", r>>8, g>>8, b>>8, shot.Width, shot.Height)
	}
	full, err := a.client.ScreenCaptureStream(ctx, &client.CaptureRequest{Cursor: client.Ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	full.Close()
	if full.ContentType != "image/jpeg" || full.Header.Get("X-Result-Width") == "" {
		t.Fatalf("stream headers = %v", full.Header)
	}
}

// Spec 13.1 acceptance 1 (Linux part): clicks land exactly on the
// requested physical pixel.
func TestClickAccuracy(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	info, _ := a.client.ScreenInfo(ctx)
	b := info.Displays[0].Bounds
	w := startWindow(t, xwinSpec{title: "click-grid", x: b.X, y: b.Y, w: b.W, h: b.H})
	a.client.WindowActivate(ctx, &client.WindowSelector{ID: client.Ptr(w.id)})
	var points []client.Point
	for _, fx := range []float64{0.05, 0.3, 0.5, 0.77, 0.95} {
		for _, fy := range []float64{0.07, 0.5, 0.93} {
			points = append(points, client.Point{X: b.X + int(fx*float64(b.W)), Y: b.Y + int(fy*float64(b.H))})
		}
	}
	for _, p := range points {
		if err := a.client.MouseClick(ctx, &client.MouseClickRequest{X: client.Ptr(p.X), Y: client.Ptr(p.Y)}); err != nil {
			t.Fatal(err)
		}
	}
	got := w.buttons(t, len(points))
	if len(got) != len(points) {
		t.Fatalf("got %d clicks, want %d", len(got), len(points))
	}
	worst := 0
	for i, p := range points {
		d := max(abs(got[i].X-p.X), abs(got[i].Y-p.Y))
		worst = max(worst, d)
		if d > 2 {
			t.Errorf("click %d at %+v landed at (%d,%d)", i, p, got[i].X, got[i].Y)
		}
	}
	t.Logf("max click error over %d points: %d px", len(points), worst)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func keysymRune(k uint32) rune {
	switch {
	case k >= 0x01000000:
		return rune(k - 0x01000000)
	case k == 0xff0d:
		return '\n'
	case k == 0xff09:
		return '\t'
	}
	return rune(k)
}

func TestTypeUnicodeAndKeys(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	w := startWindow(t, xwinSpec{title: "typing-target", x: 50, y: 50})
	if _, err := a.client.WindowActivate(ctx, &client.WindowSelector{ID: client.Ptr(w.id)}); err != nil {
		t.Fatal(err)
	}
	// The pointer must be over the window for focus-follows-mouse setups.
	win, _ := a.client.WindowActive(ctx)
	a.client.MouseMove(ctx, &client.MouseMoveRequest{X: win.ClientRect.X + 20, Y: win.ClientRect.Y + 20})
	text := "Hello, 世界! ~é\n"
	if err := a.client.KeyType(ctx, &client.KeyTypeRequest{Text: text}); err != nil {
		t.Fatal(err)
	}
	if err := a.client.KeyType(ctx, &client.KeyTypeRequest{Text: "Az09", Method: client.Ptr("scancode")}); err != nil {
		t.Fatal(err)
	}
	want := []rune(text + "Az09")
	var got []rune
	for _, e := range w.eventsAfter(t, 60, 5*time.Second) {
		if e.Type != "key" || (e.Keysym >= 0xffe1 && e.Keysym <= 0xffee) { // modifiers
			continue
		}
		got = append(got, keysymRune(e.Keysym))
	}
	if string(got) != string(want) {
		t.Fatalf("typed %q, window saw %q", string(want), string(got))
	}
}

// Spec 13.1 acceptance 4: a repeated request id does not click twice.
func TestDuplicateRequestIDClicksOnce(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	w := startWindow(t, xwinSpec{title: "dedup-target", x: 100, y: 100})
	cr := activate(t, a, w)
	req := &client.MouseClickRequest{X: client.Ptr(cr.X + 50), Y: client.Ptr(cr.Y + 50)}
	var info client.ResponseInfo
	for i := 0; i < 3; i++ {
		if err := a.client.MouseClick(ctx, req, client.WithRequestID("dedup-click-000000001"), client.WithResponseInfo(&info)); err != nil {
			t.Fatal(err)
		}
	}
	if !info.DedupHit {
		t.Fatal("repeat was not a dedup hit")
	}
	time.Sleep(300 * time.Millisecond)
	if got := w.buttons(t, 1); len(got) != 1 {
		t.Fatalf("window saw %d clicks, want 1", len(got))
	}
}

// Spec 13.2 acceptance 5: a 50-action batch runs in order and a failure part
// way reports the exact executed count.
func TestBatchOrderAndExecutedCount(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	w := startWindow(t, xwinSpec{title: "batch-target", x: 0, y: 0, w: 1000, h: 700})
	cr := activate(t, a, w)
	var actions []client.BatchAction
	for i := 0; i < 50; i++ {
		actions = append(actions, client.BatchAction{Type: "mouse.click", X: client.Ptr(cr.X + 20 + i*10), Y: client.Ptr(cr.Y + 300 + i%7)})
	}
	res, err := a.client.InputBatch(ctx, &client.BatchRequest{Actions: actions})
	if err != nil || res.Executed != 50 {
		t.Fatalf("batch = %+v, %v", res, err)
	}
	got := w.buttons(t, 50)
	if len(got) != 50 {
		var xs []int
		for _, e := range got {
			xs = append(xs, e.X-cr.X)
		}
		t.Fatalf("window saw %d clicks: %v", len(got), xs)
	}
	for i, e := range got {
		if e.X != cr.X+20+i*10 {
			t.Fatalf("click %d at x=%d, want %d (out of order)", i, e.X, cr.X+20+i*10)
		}
	}
	// Action 30 fails at run time (outside the X coordinate range).
	failing := append([]client.BatchAction(nil), actions...)
	failing[30] = client.BatchAction{Type: "mouse.move", X: client.Ptr(70000), Y: client.Ptr(0)}
	_, err = a.client.InputBatch(ctx, &client.BatchRequest{Actions: failing})
	e := apiErr(t, err)
	if e.Details["executed"] != float64(30) || e.Details["index"] != float64(30) {
		t.Fatalf("failure details = %+v", e.Details)
	}
	if got := w.buttons(t, 80); len(got) != 80 {
		t.Fatalf("window saw %d clicks after the failing batch, want 80", len(got))
	}
}

// Spec section 12: a client disconnecting mid-batch stops execution and the
// audit records the executed count.
func TestBatchStopsWhenClientDisconnects(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	w := startWindow(t, xwinSpec{title: "disconnect-target", x: 0, y: 0, w: 800, h: 600})
	cr := activate(t, a, w)
	var actions []client.BatchAction
	for i := 0; i < 100; i++ {
		actions = append(actions, client.BatchAction{Type: "mouse.click", X: client.Ptr(cr.X + 100 + i), Y: client.Ptr(cr.Y + 200), DelayAfterMS: client.Ptr(20)})
	}
	cctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_, err := a.client.InputBatch(cctx, &client.BatchRequest{Actions: actions}, client.WithRequestID("disconnect-batch-0001"))
	if err == nil {
		t.Fatal("batch finished despite the disconnect")
	}
	time.Sleep(500 * time.Millisecond)
	clicks := len(w.buttons(t, 0))
	var executed string
	for _, ev := range a.auditEvents(t) {
		if ev["action"] == "input.batch" {
			if p, ok := ev["params"].(map[string]any); ok {
				executed, _ = p["executed"].(string)
			}
		}
	}
	if executed == "" || executed == "100" || executed != strconv.Itoa(clicks) {
		t.Fatalf("audit executed=%q, window saw %d clicks", executed, clicks)
	}
	// A retry with the same id replays the recorded outcome instead of
	// clicking again.
	_, err = a.client.InputBatch(ctx, &client.BatchRequest{Actions: actions}, client.WithRequestID("disconnect-batch-0001"))
	if e := apiErr(t, err); e.Reason() != "client_disconnected" || e.Details["executed"] != float64(clicks) {
		t.Fatalf("replay = %+v", e)
	}
	if after := len(w.buttons(t, 0)); after != clicks {
		t.Fatalf("replay clicked again: %d -> %d", clicks, after)
	}
}

// activate brings w to the front and returns its client rectangle; the
// window manager decides the final position.
func activate(t *testing.T, a *agent, w *xwin) client.Rect {
	t.Helper()
	win, err := a.client.WindowActivate(ctx, &client.WindowSelector{ID: client.Ptr(w.id)})
	if err != nil {
		t.Fatal(err)
	}
	if win.ClientRect == nil {
		t.Fatalf("window %s has no client rect", w.title)
	}
	return *win.ClientRect
}

func TestWindowListActivateClose(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	w1 := startWindow(t, xwinSpec{title: "it-window-one", class: "ITOne", x: 10, y: 10})
	w2 := startWindow(t, xwinSpec{title: "it-window-two", class: "ITTwo", x: 300, y: 200})
	list, err := a.client.WindowList(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]client.Window{}
	for _, w := range list.Windows {
		found[w.Title] = w
	}
	one, ok := found["it-window-one"]
	if !ok || one.Class != "ITOne" || one.PID != w1.cmd.Process.Pid || one.Rect == nil || one.ClientRect == nil {
		t.Fatalf("window one = %+v (all: %v)", one, found)
	}
	if one.Rect.W < one.ClientRect.W || one.Rect.H < one.ClientRect.H {
		t.Fatalf("frame %+v smaller than client %+v", one.Rect, one.ClientRect)
	}
	for _, target := range []*xwin{w1, w2, w1} {
		got, err := a.client.WindowActivate(ctx, &client.WindowSelector{Title: client.Ptr(target.title)})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Active || got.ID != target.id {
			t.Fatalf("activate %s = %+v", target.title, got)
		}
		active, _ := a.client.WindowActive(ctx)
		if active.ID != target.id {
			t.Fatalf("active = %s, want %s", active.ID, target.id)
		}
	}
	_, err = a.client.WindowActivate(ctx, &client.WindowSelector{Title: client.Ptr("it-window-"), Match: client.Ptr("contains")})
	if e := apiErr(t, err); e.Code != "ambiguous" || len(e.Details["candidates"].([]any)) != 2 {
		t.Fatalf("ambiguous = %+v", e)
	}
	if _, err := a.client.WindowClose(ctx, &client.WindowSelector{Title: client.Ptr("it-window-two")}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w2.done:
	case <-time.After(5 * time.Second):
		t.Fatal("window two did not close")
	}
}

// Spec 13.1 acceptance 6: after a restart (crash or graceful) programs from
// process/launch keep running while commands from exec are gone.
func TestLaunchSurvivesRestartExecDoesNot(t *testing.T) {
	requireX(t)
	for _, mode := range []string{"crash", "graceful"} {
		t.Run(mode, func(t *testing.T) {
			a := startAgent(t)
			launched, err := a.client.ProcessLaunch(ctx, &client.LaunchRequest{Argv: []string{"sleep", "300"}})
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Kill(launched.PID, syscall.SIGKILL)
			marker := filepath.Join(t.TempDir(), "exec.pid")
			go a.client.Exec(ctx, &client.ExecRequest{Argv: []string{"sh", "-c", "sleep 300 & echo $! > " + marker + "; wait"}}, client.WithTimeout(10*time.Minute))
			var execPid int
			for i := 0; i < 200 && execPid == 0; i++ {
				data, _ := os.ReadFile(marker)
				execPid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
				time.Sleep(20 * time.Millisecond)
			}
			if execPid == 0 {
				t.Fatal("exec never started")
			}
			if mode == "crash" {
				a.crash()
			} else {
				a.stop()
			}
			a.start() // the restarted agent reaps orphaned command trees
			deadline := time.Now().Add(5 * time.Second)
			for syscall.Kill(execPid, 0) == nil && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
			}
			if syscall.Kill(execPid, 0) == nil {
				t.Fatalf("exec descendant %d survived the %s restart", execPid, mode)
			}
			if err := syscall.Kill(launched.PID, 0); err != nil {
				t.Fatalf("launched program %d died with the agent: %v", launched.PID, err)
			}
		})
	}
}

func TestExecAndFilesOnRealSystem(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	res, err := a.client.Exec(ctx, &client.ExecRequest{Argv: []string{"sh", "-c", "echo $FOO; exit 4"}, Env: map[string]string{"FOO": "bar"}})
	if err != nil || *res.ExitCode != 4 || *res.Stdout != "bar\n" {
		t.Fatalf("exec = %+v, %v", res, err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "x", "note.txt")
	if _, err := a.client.FileWrite(ctx, &client.FileWriteRequest{Path: p, Content: []byte("content"), Mkdir: client.Ptr(true)}); err != nil {
		t.Fatal(err)
	}
	r, err := a.client.FileRead(ctx, &client.FileReadRequest{Path: p})
	if err != nil || string(r.Content) != "content" {
		t.Fatalf("read = %+v, %v", r, err)
	}
	if d, err := a.client.FileKnownDirs(ctx); err != nil || d.Home == "" {
		t.Fatalf("known dirs = %+v, %v", d, err)
	}
}

func TestShutdownWritesAuditRecords(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	a.client.Probe(ctx)
	a.stop()
	kinds := []string{}
	for _, ev := range a.auditEvents(t) {
		kinds = append(kinds, fmt.Sprint(ev["kind"]))
	}
	if kinds[0] != "startup" || kinds[len(kinds)-1] != "shutdown" {
		t.Fatalf("audit kinds = %v", kinds)
	}
}
