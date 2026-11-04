//go:build linux

package integration

import (
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

// Spec 13.2 acceptance 3: close_all never closes desktop shell windows or
// vm-agent's own windows.
func TestCloseAllSparesShellAndSelf(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	user1 := startWindow(t, xwinSpec{title: "closeall-user-1", x: 10, y: 10})
	user2 := startWindow(t, xwinSpec{title: "closeall-user-2", x: 300, y: 10})
	kept := startWindow(t, xwinSpec{title: "closeall-keep-by-title", x: 600, y: 10})
	desktop := startWindow(t, xwinSpec{title: "closeall-desktop", winType: "DESKTOP", x: 0, y: 0, w: 200, h: 100})
	dock := startWindow(t, xwinSpec{title: "closeall-dock", winType: "DOCK", x: 0, y: 900, w: 400, h: 40})
	tool := startWindow(t, xwinSpec{title: "closeall-tool", winType: "UTILITY", x: 900, y: 10})
	self := startWindow(t, xwinSpec{title: "closeall-agent-own", pid: a.cmd.Process.Pid, x: 900, y: 400})

	res, err := a.client.WindowCloseAll(ctx, &client.CloseAllRequest{ExcludeTitles: []string{"keep-by-title"}, WaitMS: client.Ptr(3000)})
	if err != nil {
		t.Fatal(err)
	}
	closed := map[string]bool{}
	for _, w := range res.Closed {
		closed[w.Title] = true
	}
	if !closed["closeall-user-1"] || !closed["closeall-user-2"] {
		t.Fatalf("user windows not closed: %+v", res)
	}
	for _, w := range []*xwin{user1, user2} {
		select {
		case <-w.done:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s still running", w.title)
		}
	}
	for _, w := range []*xwin{kept, desktop, dock, tool, self} {
		select {
		case <-w.done:
			t.Fatalf("%s was closed", w.title)
		default:
		}
		if closed[w.title] {
			t.Fatalf("%s reported closed", w.title)
		}
	}
}

func TestWindowResizeAndStates(t *testing.T) {
	requireX(t)
	a := startAgent(t)
	w := startWindow(t, xwinSpec{title: "resize-target", x: 50, y: 50, w: 400, h: 300})
	rect := client.Rect{X: 120, Y: 140, W: 640, H: 480}
	got, err := a.client.WindowResize(ctx, &client.WindowResizeRequest{ID: w.id, Rect: &rect})
	if err != nil {
		t.Fatal(err)
	}
	if got.Rect == nil || *got.Rect != rect {
		t.Fatalf("after resize rect = %+v, want %+v", got.Rect, rect)
	}
	got, err = a.client.WindowResize(ctx, &client.WindowResizeRequest{ID: w.id, State: client.Ptr("maximize")})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := a.client.ScreenInfo(ctx)
	if got.Rect == nil || got.Rect.W < info.Displays[0].Bounds.W*9/10 {
		t.Fatalf("maximized rect = %+v", got.Rect)
	}
	// rect on a maximized window restores it first.
	got, err = a.client.WindowResize(ctx, &client.WindowResizeRequest{ID: w.id, Rect: &rect})
	if err != nil || got.Rect == nil || *got.Rect != rect {
		t.Fatalf("rect after maximize = %+v, %v", got.Rect, err)
	}
	got, err = a.client.WindowResize(ctx, &client.WindowResizeRequest{ID: w.id, State: client.Ptr("minimize")})
	if err != nil || !got.Minimized || got.Rect != nil {
		t.Fatalf("minimized = %+v, %v", got, err)
	}
	got, err = a.client.WindowResize(ctx, &client.WindowResizeRequest{ID: w.id, State: client.Ptr("restore")})
	if err != nil || got.Minimized || got.Rect == nil {
		t.Fatalf("restored = %+v, %v", got, err)
	}
}
