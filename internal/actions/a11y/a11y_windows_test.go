//go:build windows

package a11y

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/platform/win"
)

// Notepad is a classic Win32 application: through the native UI Automation
// core its text area is an edit (or document) control holding the text and
// its frame has a title bar, which the managed client lost on machines
// without its Win32 proxies.
func TestCollectNotepad(t *testing.T) {
	cmd := exec.Command("notepad.exe")
	if err := cmd.Start(); err != nil {
		t.Skipf("no notepad: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var hwnd win.HWND
	for deadline := time.Now().Add(15 * time.Second); hwnd == 0 && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		wins, _ := win.TopLevelWindows()
		for _, h := range wins {
			if _, pid := win.ThreadProcess(h); int(pid) == cmd.Process.Pid && win.IsVisible(h) {
				hwnd = h
			}
		}
	}
	if hwnd == 0 {
		t.Skip("notepad opened no window (no interactive desktop?)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := Collect(ctx, Request{Target: Target{ID: fmt.Sprintf("0x%x", uintptr(hwnd))}, MaxDepth: DefaultMaxDepth, MaxNodes: DefaultMaxNodes})
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]int{}
	var count func(n *Node) int
	count = func(n *Node) int {
		roles[n.Role]++
		c := 1
		for _, ch := range n.Children {
			c += count(ch)
		}
		return c
	}
	if got := count(res.Root); got != res.Nodes {
		t.Fatalf("tree has %d nodes, result says %d", got, res.Nodes)
	}
	if res.Root.Role != "window" || res.Root.Rect == nil || roles["edit"]+roles["document"] == 0 || roles["title_bar"] == 0 {
		t.Fatalf("root %s %v, roles %v", res.Root.Role, res.Root.Rect, roles)
	}

	small, err := Collect(ctx, Request{Target: Target{ID: fmt.Sprintf("0x%x", uintptr(hwnd))}, MaxDepth: 1, MaxNodes: DefaultMaxNodes})
	if err != nil || small.Nodes != 1 || !small.Truncated || len(small.Root.Children) != 0 {
		t.Fatalf("max_depth 1: %+v %v", small, err)
	}
}
