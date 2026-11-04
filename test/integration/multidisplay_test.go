//go:build linux

package integration

import (
	"bytes"
	"fmt"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

// monitor is one output of a layout, in root-window coordinates.
type monitor struct {
	output     string
	x, y, w, h int
	primary    bool
	color      color.RGBA
	// mmScale, when set, declares a physical size giving this DPI scale.
	mmScale float64
}

type layout struct {
	name     string
	monitors []monitor
}

var (
	red    = color.RGBA{220, 30, 30, 255}
	green  = color.RGBA{30, 200, 60, 255}
	blue   = color.RGBA{40, 60, 230, 255}
	yellow = color.RGBA{230, 210, 30, 255}
)

func modeFor(w, h int) string { return fmt.Sprintf("%dx%d", w, h) }

var layouts = []layout{
	{"single", []monitor{{output: "DUMMY0", x: 0, y: 0, w: 1920, h: 1080, primary: true, color: red}}},
	{"primary-left", []monitor{
		{output: "DUMMY0", x: 0, y: 0, w: 1920, h: 1080, primary: true, color: red},
		{output: "DUMMY1", x: 1920, y: 0, w: 1280, h: 1024, color: green},
	}},
	{"primary-right-offset", []monitor{
		{output: "DUMMY1", x: 0, y: 0, w: 1280, h: 1024, color: green},
		{output: "DUMMY0", x: 1280, y: 200, w: 1920, h: 1080, primary: true, color: red},
	}},
	{"three-wide", []monitor{
		{output: "DUMMY1", x: 0, y: 0, w: 1280, h: 1024, color: green},
		{output: "DUMMY0", x: 1280, y: 200, w: 1920, h: 1080, primary: true, color: red},
		{output: "DUMMY2", x: 3200, y: 0, w: 1024, h: 768, color: blue},
	}},
	{"secondary-above", []monitor{
		{output: "DUMMY2", x: 288, y: 0, w: 1024, h: 768, color: blue},
		{output: "DUMMY0", x: 0, y: 768, w: 1600, h: 900, primary: true, color: yellow},
	}},
	{"mixed-scale", []monitor{
		{output: "DUMMY0", x: 0, y: 0, w: 1920, h: 1080, primary: true, color: red},
		{output: "DUMMY1", x: 1920, y: 0, w: 1280, h: 1024, color: green, mmScale: 2},
	}},
}

func xrandr(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("xrandr", args...).CombinedOutput(); err != nil {
		t.Fatalf("xrandr %v: %v\n%s", args, err, out)
	}
}

// apply configures the outputs of l and switches the others off.
func apply(t *testing.T, l layout) {
	t.Helper()
	fbW, fbH := 0, 0
	used := map[string]bool{}
	args := []string{}
	for _, m := range l.monitors {
		used[m.output] = true
		fbW, fbH = max(fbW, m.x+m.w), max(fbH, m.y+m.h)
		exec.Command("xrandr", "--addmode", m.output, modeFor(m.w, m.h)).Run()
		args = append(args, "--output", m.output, "--mode", modeFor(m.w, m.h), "--pos", fmt.Sprintf("%dx%d", m.x, m.y))
		if m.primary {
			args = append(args, "--primary")
		}
	}
	for _, o := range []string{"DUMMY0", "DUMMY1", "DUMMY2"} {
		if !used[o] {
			args = append(args, "--output", o, "--off")
		}
	}
	// Drop monitors declared by an earlier layout.
	if out, err := exec.Command("xrandr", "--listmonitors").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && strings.HasPrefix(f[len(f)-1], "VMA") {
				exec.Command("xrandr", "--delmonitor", f[len(f)-1]).Run()
			}
		}
	}
	xrandr(t, append([]string{"--fb", fmt.Sprintf("%dx%d", fbW, fbH)}, args...)...)
	for i, m := range l.monitors {
		if m.mmScale > 0 {
			mmW, mmH := int(float64(m.w)*25.4/96/m.mmScale), int(float64(m.h)*25.4/96/m.mmScale)
			xrandr(t, "--setmonitor", fmt.Sprintf("VMA%d", i), fmt.Sprintf("%d/%dx%d/%d+%d+%d", m.w, mmW, m.h, mmH, m.x, m.y), m.output)
		}
	}
}

func primaryOf(l layout) monitor {
	for _, m := range l.monitors {
		if m.primary {
			return m
		}
	}
	panic("no primary")
}

// api converts root coordinates to API coordinates.
func api(l layout, x, y int) (int, int) {
	p := primaryOf(l)
	return x - p.x, y - p.y
}

func pixel(t *testing.T, data []byte, x, y int) color.RGBA {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(img.Bounds().Min.X+x, img.Bounds().Min.Y+y).RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 255}
}

func near(a, b color.RGBA) bool {
	d := func(x, y uint8) int { return abs(int(x) - int(y)) }
	return d(a.R, b.R)+d(a.G, b.G)+d(a.B, b.B) < 30
}

// TestMultiDisplayMatrix runs against an Xorg dummy server (spec 13.3):
// every layout checks display enumeration, per-display and virtual-desktop
// capture, a region spanning monitors, clicks and pointer positions on
// each monitor (including negative coordinates), and window rectangles.
// It needs VM_AGENT_MULTIDISPLAY=1 and DISPLAY on an Xorg dummy server
// without a window manager (deploy/ci/multi-display.sh).
func TestMultiDisplayMatrix(t *testing.T) {
	requireX(t)
	if os.Getenv("VM_AGENT_MULTIDISPLAY") != "1" {
		t.Skip("set VM_AGENT_MULTIDISPLAY=1 with DISPLAY on an Xorg dummy server")
	}
	a := startAgent(t)
	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			apply(t, l)
			// Displays, primary first, in API coordinates.
			var info *client.ScreenInfoResult
			deadline := time.Now().Add(5 * time.Second)
			for {
				var err error
				info, err = a.client.ScreenInfo(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(info.Displays) == len(l.monitors) || time.Now().After(deadline) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if len(info.Displays) != len(l.monitors) {
				t.Fatalf("%d displays, want %d: %+v", len(info.Displays), len(l.monitors), info.Displays)
			}
			if p := info.Displays[0]; !p.Primary || p.Bounds.X != 0 || p.Bounds.Y != 0 || p.Bounds.W != primaryOf(l).w {
				t.Fatalf("display 0 = %+v, want the primary at the origin", p)
			}
			byOrigin := map[[2]int]client.Display{}
			for _, d := range info.Displays {
				byOrigin[[2]int{d.Bounds.X, d.Bounds.Y}] = d
			}
			var wins []*xwin
			for _, m := range l.monitors {
				ax, ay := api(l, m.x, m.y)
				d, ok := byOrigin[[2]int{ax, ay}]
				if !ok || d.Bounds.W != m.w || d.Bounds.H != m.h {
					t.Fatalf("monitor %s at api %d,%d missing or wrong: %+v", m.output, ax, ay, info.Displays)
				}
				wantScale := 1.0
				if m.mmScale > 0 {
					wantScale = m.mmScale
				}
				if d.Scale != wantScale {
					t.Errorf("monitor %s scale %.2f, want %.2f", m.output, d.Scale, wantScale)
				}
				wins = append(wins, startWindow(t, xwinSpec{title: "md-" + m.output, x: m.x, y: m.y, w: m.w, h: m.h,
					color: fmt.Sprintf("%02x%02x%02x", m.color.R, m.color.G, m.color.B)}))
			}
			time.Sleep(300 * time.Millisecond)

			// Each display captures its own monitor.
			for _, d := range info.Displays {
				shot, err := a.client.ScreenCapture(ctx, &client.CaptureRequest{Display: client.Ptr(d.Index), Format: client.Ptr("png")})
				if err != nil {
					t.Fatal(err)
				}
				if shot.Width != d.Bounds.W || shot.Height != d.Bounds.H {
					t.Fatalf("display %d capture %dx%d, want %dx%d", d.Index, shot.Width, shot.Height, d.Bounds.W, d.Bounds.H)
				}
				m := l.monitors[indexAt(l, d.Bounds.X, d.Bounds.Y)]
				if got := pixel(t, shot.Image, d.Bounds.W/2, d.Bounds.H/2); !near(got, m.color) {
					t.Errorf("display %d center %v, want %v", d.Index, got, m.color)
				}
			}
			// The virtual desktop is the bounding box; each monitor's center
			// is where it belongs.
			virt, err := a.client.ScreenCapture(ctx, &client.CaptureRequest{Display: client.Ptr(-1), Format: client.Ptr("png")})
			if err != nil {
				t.Fatal(err)
			}
			minX, minY, maxX, maxY := 1<<30, 1<<30, -1<<30, -1<<30
			for _, d := range info.Displays {
				minX, minY = min(minX, d.Bounds.X), min(minY, d.Bounds.Y)
				maxX, maxY = max(maxX, d.Bounds.X+d.Bounds.W), max(maxY, d.Bounds.Y+d.Bounds.H)
			}
			if virt.Width != maxX-minX || virt.Height != maxY-minY {
				t.Fatalf("virtual desktop %dx%d, want %dx%d", virt.Width, virt.Height, maxX-minX, maxY-minY)
			}
			for _, d := range info.Displays {
				m := l.monitors[indexAt(l, d.Bounds.X, d.Bounds.Y)]
				cx, cy := d.Bounds.X+d.Bounds.W/2-minX, d.Bounds.Y+d.Bounds.H/2-minY
				if got := pixel(t, virt.Image, cx, cy); !near(got, m.color) {
					t.Errorf("virtual desktop at display %d center %v, want %v", d.Index, got, m.color)
				}
			}
			// A region spanning the primary and a neighbour on the virtual desktop.
			if len(l.monitors) > 1 {
				other := info.Displays[1]
				rx := min(other.Bounds.X+other.Bounds.W/2, info.Displays[0].Bounds.W/2)
				ry := max(other.Bounds.Y, 0) + 10
				region := client.Rect{X: rx - 40, Y: ry, W: 80, H: 20}
				if other.Bounds.Y+other.Bounds.H <= 0 { // stacked vertically
					region = client.Rect{X: max(other.Bounds.X, 0) + 10, Y: -10, W: 20, H: 20}
				}
				shot, err := a.client.ScreenCapture(ctx, &client.CaptureRequest{Display: client.Ptr(-1), Region: &region, Format: client.Ptr("png")})
				if err != nil {
					t.Fatal(err)
				}
				if shot.Width != region.W || shot.Height != region.H {
					t.Fatalf("spanning region %dx%d, want %dx%d", shot.Width, shot.Height, region.W, region.H)
				}
			}
			// Pointer and clicks land on the requested physical pixel of
			// every monitor, negative coordinates included.
			for i, m := range l.monitors {
				ax, ay := api(l, m.x+m.w/3, m.y+m.h/3)
				if err := a.client.MouseMove(ctx, &client.MouseMoveRequest{X: ax, Y: ay}); err != nil {
					t.Fatal(err)
				}
				cur, err := a.client.ScreenCursor(ctx)
				if err != nil || cur.X != ax || cur.Y != ay {
					t.Fatalf("cursor %+v, want %d,%d (%v)", cur, ax, ay, err)
				}
				if err := a.client.MouseClick(ctx, &client.MouseClickRequest{X: client.Ptr(ax), Y: client.Ptr(ay)}); err != nil {
					t.Fatal(err)
				}
				got := wins[i].buttons(t, 1)
				if len(got) != 1 || got[0].X != m.x+m.w/3 || got[0].Y != m.y+m.h/3 {
					t.Fatalf("click on %s at api %d,%d arrived as %+v, want root %d,%d", m.output, ax, ay, got, m.x+m.w/3, m.y+m.h/3)
				}
			}
			// Window rectangles are reported in API coordinates.
			list, err := a.client.WindowList(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range l.monitors {
				ax, ay := api(l, m.x, m.y)
				found := false
				for _, w := range list.Windows {
					if w.Title == "md-"+m.output {
						found = true
						if w.Rect == nil || w.Rect.X != ax || w.Rect.Y != ay || w.Rect.W != m.w {
							t.Errorf("window on %s rect %+v, want %d,%d %dx%d", m.output, w.Rect, ax, ay, m.w, m.h)
						}
					}
				}
				if !found {
					t.Errorf("window on %s not listed", m.output)
				}
			}
			for _, w := range wins {
				w.cmd.Process.Kill()
				<-w.done
			}
		})
	}
	apply(t, layouts[0])
}

func indexAt(l layout, apiX, apiY int) int {
	for i, m := range l.monitors {
		if x, y := api(l, m.x, m.y); x == apiX && y == apiY {
			return i
		}
	}
	panic("no monitor at the API origin")
}
