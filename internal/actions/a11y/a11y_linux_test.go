//go:build linux

package a11y

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"

	"github.com/CIPFZ/go-agent-platform/internal/platform/x11"
)

func TestStateNames(t *testing.T) {
	bits := uint64(1)<<8 | 1<<12 | 1<<25 | 1<<43
	got := strings.Join(stateNames(bits), ",")
	if got != "enabled,focused,read_only" {
		t.Fatalf("states = %s", got)
	}
	if got := strings.Join(stateNames(0), ","); got != "offscreen" {
		t.Fatalf("hidden states = %s", got)
	}
}

// With VM_AGENT_INTEGRATION=1, a DISPLAY, a session bus with AT-SPI and
// mousepad installed, the helper finds mousepad's window and its text.
func TestCollectFromMousepad(t *testing.T) {
	if os.Getenv("VM_AGENT_INTEGRATION") != "1" || os.Getenv("DISPLAY") == "" || os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Skip("needs VM_AGENT_INTEGRATION=1, DISPLAY and a session bus")
	}
	if _, err := exec.LookPath("mousepad"); err != nil {
		t.Skip("mousepad not installed")
	}
	if !Available() {
		t.Skip("AT-SPI bus not available")
	}
	doc := t.TempDir() + "/a11y-doc.txt"
	os.WriteFile(doc, []byte("accessible text 世界"), 0o644)
	cmd := exec.Command("mousepad", doc)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	var target Target
	deadline := time.Now().Add(15 * time.Second)
	for target.ID == "" && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		target = findX11Window(t, cmd.Process.Pid)
	}
	if target.ID == "" {
		t.Fatal("mousepad window not found")
	}
	time.Sleep(time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := Collect(ctx, Request{Target: target, MaxDepth: 30, MaxNodes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if res.Root.Role != "frame" || !strings.Contains(res.Root.Name, "a11y-doc.txt") || res.Root.Rect == nil || res.Nodes < 5 {
		t.Fatalf("root = %+v nodes %d", res.Root, res.Nodes)
	}
	var found *Node
	var visit func(n *Node)
	visit = func(n *Node) {
		if strings.Contains(n.Value, "accessible text 世界") {
			found = n
		}
		for _, c := range n.Children {
			visit(c)
		}
	}
	visit(res.Root)
	if found == nil {
		t.Fatal("the document text is not in the tree")
	}
	t.Logf("%d nodes; text node role %s states %v rect %+v", res.Nodes, found.Role, found.State, found.Rect)

	small, err := Collect(ctx, Request{Target: target, MaxDepth: 2, MaxNodes: 3})
	if err != nil || !small.Truncated || small.Nodes > 3 {
		t.Fatalf("limits: %+v %v", small, err)
	}
	_, err = Collect(ctx, Request{Target: Target{ID: "0x1", PID: 1 << 22}, MaxDepth: 5, MaxNodes: 10})
	if err == nil || !strings.Contains(err.Error(), "no accessible application") {
		t.Fatalf("missing window err = %v", err)
	}
}

func findX11Window(t *testing.T, pid int) Target {
	c, err := x11.Shared()
	if err != nil {
		t.Fatal(err)
	}
	tree, err := xproto.QueryTree(c.X, c.Root).Reply()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range tree.Children {
		reply, err := c.Property(w, "_NET_WM_PID")
		if err != nil {
			continue
		}
		if v := x11.PropertyCardinals(reply); len(v) == 1 && int(v[0]) == pid {
			name, _ := c.Property(w, "_NET_WM_NAME")
			title := ""
			if name != nil {
				title = string(name.Value)
			}
			if title != "" {
				return Target{ID: "0x" + strings.TrimLeft(strings.ToLower(hex(uint32(w))), "0"), Title: title, PID: pid}
			}
		}
	}
	return Target{}
}

func hex(v uint32) string {
	const d = "0123456789abcdef"
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = d[v&0xf]
		v >>= 4
	}
	return string(out)
}
