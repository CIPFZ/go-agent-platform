//go:build linux

package integration

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

func TestClipboardWithOtherClients(t *testing.T) {
	requireX(t)
	if _, err := exec.LookPath("xclip"); err != nil {
		t.Skip("xclip is not installed")
	}
	a := startAgent(t)
	if err := a.client.ClipboardWrite(ctx, &client.ClipboardWriteRequest{Text: client.Ptr("agent → xclip")}); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("xclip", "-o", "-selection", "clipboard").Output()
	if err != nil || string(out) != "agent → xclip" {
		t.Fatalf("xclip read %q %v", out, err)
	}
	cmd := exec.Command("xclip", "-i", "-selection", "clipboard")
	cmd.Stdin = strings.NewReader("xclip → agent")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	res, err := a.client.ClipboardRead(ctx)
	if err != nil || res.Text == nil || *res.Text != "xclip → agent" || len(res.Formats) == 0 {
		t.Fatalf("read = %+v %v", res, err)
	}
}
