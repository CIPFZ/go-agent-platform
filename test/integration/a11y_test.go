//go:build linux

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

// a11y/tree through the real agent binary, which re-executes itself as the
// AT-SPI helper; needs a session bus with AT-SPI and mousepad.
func TestA11yTreeOfEditor(t *testing.T) {
	requireX(t)
	if _, err := exec.LookPath("mousepad"); err != nil {
		t.Skip("mousepad not installed")
	}
	a := startAgent(t)
	caps, _ := a.client.Capabilities(ctx)
	if !strings.Contains(strings.Join(caps.Actions, ","), "a11y") {
		t.Skip("a11y not available (no AT-SPI bus)")
	}
	doc := filepath.Join(t.TempDir(), "tree.txt")
	os.WriteFile(doc, []byte("tree text"), 0o644)
	launched, err := a.client.ProcessLaunch(ctx, &client.LaunchRequest{Argv: []string{"mousepad", doc}})
	if err != nil {
		t.Fatal(err)
	}
	defer exec.Command("kill", "-9", strings.TrimSpace(strconvItoa(launched.PID))).Run()
	var winID string
	for i := 0; i < 60 && winID == ""; i++ {
		time.Sleep(250 * time.Millisecond)
		list, _ := a.client.WindowList(ctx, nil)
		for _, w := range list.Windows {
			if strings.Contains(w.Title, "tree.txt") {
				winID = w.ID
			}
		}
	}
	if winID == "" {
		t.Fatal("editor window not found")
	}
	if _, err := a.client.WindowActivate(ctx, &client.WindowSelector{ID: client.Ptr(winID)}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	tree, err := a.client.A11yTree(ctx, &client.A11yTreeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if tree.Window.ID != winID || tree.Root.Role != "frame" || tree.Nodes < 5 || tree.Root.Rect == nil {
		t.Fatalf("tree window %+v root %s/%s nodes %d", tree.Window, tree.Root.Role, tree.Root.Name, tree.Nodes)
	}
	// The root frame's rect matches the window's frame rectangle.
	win, _ := a.client.WindowActive(ctx)
	if d := abs(tree.Root.Rect.X-win.Rect.X) + abs(tree.Root.Rect.Y-win.Rect.Y); d > 60 {
		t.Fatalf("a11y rect %+v vs window rect %+v", tree.Root.Rect, win.Rect)
	}
}

func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}
