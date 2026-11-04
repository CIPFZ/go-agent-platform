//go:build linux

package integration

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

// wevEvents parses wev's log: pointer motions, button presses and the
// UTF-8 text of key presses (deduplicated by serial, as wev reports each
// key once per wl_keyboard it bound).
type wevLog struct {
	motions [][2]float64
	presses []int
	text    string
	axes    int
}

var (
	wevMotion = regexp.MustCompile(`wl_pointer\] motion: .*x, y: ([0-9.]+), ([0-9.]+)`)
	wevButton = regexp.MustCompile(`wl_pointer\] button: .*button: (\d+) \(\w+\), state: 1`)
	wevKey    = regexp.MustCompile(`wl_keyboard\] key: serial: (\d+); .*state: 1 \(pressed\)`)
	wevUTF8   = regexp.MustCompile(`utf8: '(.*)'`)
	wevAxis   = regexp.MustCompile(`wl_pointer\] axis: `)
)

func readWev(t *testing.T, path string) wevLog {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var l wevLog
	seen := map[string]bool{}
	pendingKey := ""
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if m := wevMotion.FindStringSubmatch(line); m != nil {
			x, _ := strconv.ParseFloat(m[1], 64)
			y, _ := strconv.ParseFloat(m[2], 64)
			l.motions = append(l.motions, [2]float64{x, y})
		}
		if m := wevButton.FindStringSubmatch(line); m != nil {
			b, _ := strconv.Atoi(m[1])
			l.presses = append(l.presses, b)
		}
		if wevAxis.MatchString(line) {
			l.axes++
		}
		if m := wevKey.FindStringSubmatch(line); m != nil {
			pendingKey = m[1]
			continue
		}
		if m := wevUTF8.FindStringSubmatch(line); m != nil && pendingKey != "" {
			if !seen[pendingKey] {
				seen[pendingKey] = true
				l.text += m[1]
			}
			pendingKey = ""
		}
	}
	return l
}

func waitWev(t *testing.T, path string, ok func(wevLog) bool) wevLog {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l := readWev(t, path)
		if ok(l) || time.Now().After(deadline) {
			return l
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestWaylandScreenAndInput runs in a Wayland session (deploy/ci/wayland.sh:
// headless sway, xdg-desktop-portal with the wlr and gtk backends, uinput).
// Screen capture goes through the Screenshot portal and input through a
// uinput device; wev, covering the whole output, records what the
// compositor delivers (spec 13.3).
func TestWaylandScreenAndInput(t *testing.T) {
	if os.Getenv("VM_AGENT_WAYLAND") != "1" || os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("set VM_AGENT_WAYLAND=1 in a Wayland session (deploy/ci/wayland.sh)")
	}
	requireBinaries(t)
	a := startAgent(t)
	caps, err := a.client.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(caps.Actions, ","); got != "screen,input,exec,process,file" {
		t.Fatalf("wayland actions = %s", got)
	}
	probe, _ := a.client.Probe(ctx)
	if probe.Display.Count == 0 {
		log, _ := os.ReadFile(filepath.Join(a.dir, "agent.log"))
		info, err := a.client.ScreenInfo(ctx)
		t.Logf("agent log:\n%s\nscreen/info: %+v %v", log, info, err)
	}
	if !probe.Display.Ready || probe.Display.Count != 1 || probe.Display.Primary.W != 1920 || probe.Display.Primary.H != 1080 {
		t.Fatalf("probe display = %+v %+v", probe.Display, probe.Display.Primary)
	}

	logPath := filepath.Join(t.TempDir(), "wev.log")
	logFile, _ := os.Create(logPath)
	wev := exec.Command("wev")
	wev.Stdout, wev.Stderr = logFile, logFile
	if err := wev.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { wev.Process.Kill(); wev.Wait(); logFile.Close() }()
	time.Sleep(1500 * time.Millisecond)

	shot, err := a.client.ScreenCapture(ctx, &client.CaptureRequest{Format: client.Ptr("png")})
	if err != nil || shot.Width != 1920 || shot.Height != 1080 {
		t.Fatalf("capture = %v %v", err, shot)
	}
	region, err := a.client.ScreenCapture(ctx, &client.CaptureRequest{Region: &client.Rect{X: 100, Y: 50, W: 300, H: 200}})
	if err != nil || region.Width != 300 || region.Height != 200 {
		t.Fatalf("region capture = %v %+v", err, region)
	}

	if err := a.client.MouseMove(ctx, &client.MouseMoveRequest{X: 300, Y: 200}); err != nil {
		t.Fatal(err)
	}
	if err := a.client.MouseClick(ctx, &client.MouseClickRequest{X: client.Ptr(1500), Y: client.Ptr(900), Button: client.Ptr("right")}); err != nil {
		t.Fatal(err)
	}
	if err := a.client.MouseScroll(ctx, &client.MouseScrollRequest{DY: client.Ptr(-2)}); err != nil {
		t.Fatal(err)
	}
	if err := a.client.KeyType(ctx, &client.KeyTypeRequest{Text: "Hi there, Wayland!"}); err != nil {
		t.Fatal(err)
	}
	l := waitWev(t, logPath, func(l wevLog) bool { return strings.Contains(l.text, "Wayland!") })
	hit := func(x, y float64) bool {
		for _, m := range l.motions {
			if m[0] == x && m[1] == y {
				return true
			}
		}
		return false
	}
	if !hit(300, 200) || !hit(1500, 900) {
		t.Errorf("pointer motions %v lack (300,200) and (1500,900)", l.motions)
	}
	if len(l.presses) == 0 || l.presses[len(l.presses)-1] != 273 {
		t.Errorf("button presses %v, want the right button (273)", l.presses)
	}
	if l.axes == 0 {
		t.Error("no scroll axis events")
	}
	if l.text != "Hi there, Wayland!" {
		t.Errorf("typed text %q", l.text)
	}

	if e := apiErr(t, a.client.KeyType(ctx, &client.KeyTypeRequest{Text: "é"})); e.Code != "unsupported" {
		t.Errorf("non-ASCII through uinput = %+v", e)
	}
	for name, err := range map[string]error{
		"window/list": func() error { _, err := a.client.WindowList(ctx, nil); return err }(),
		"clipboard":   func() error { _, err := a.client.ClipboardRead(ctx); return err }(),
		"cursor":      func() error { _, err := a.client.ScreenCursor(ctx); return err }(),
		"record":      func() error { _, err := a.client.RecordStart(ctx, &client.RecordStartRequest{}); return err }(),
		"a11y":        func() error { _, err := a.client.A11yTree(ctx, &client.A11yTreeRequest{}); return err }(),
	} {
		if e := apiErr(t, err); e.Code != "unsupported" {
			t.Errorf("%s on Wayland = %+v", name, e)
		}
	}
}

func requireBinaries(t *testing.T) {
	t.Helper()
	if !enabled {
		t.Skip("set VM_AGENT_INTEGRATION=1")
	}
	buildOnce.Do(requireXBuild)
	if buildErr != nil {
		t.Fatal(buildErr)
	}
}
