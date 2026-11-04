//go:build linux

// Command xapp is a minimal X11 client used by the integration tests: it
// shows one window with a solid colour, records pointer button and key
// presses as JSON lines, and exits when asked to close via WM_DELETE_WINDOW.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

type event struct {
	Type   string `json:"type"`
	Button int    `json:"button,omitempty"`
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	Keysym uint32 `json:"keysym,omitempty"`
	State  uint16 `json:"state,omitempty"`
}

func main() {
	title := flag.String("title", "xapp", "window title")
	class := flag.String("class", "XApp", "WM_CLASS class")
	x := flag.Int("x", 0, "x")
	y := flag.Int("y", 0, "y")
	w := flag.Int("w", 400, "width")
	h := flag.Int("h", 300, "height")
	color := flag.String("color", "3366cc", "background RRGGBB")
	events := flag.String("events", "", "JSON lines event log")
	ready := flag.String("ready", "", "file created once the window is mapped")
	winType := flag.String("type", "", "_NET_WM_WINDOW_TYPE suffix, e.g. DESKTOP, DOCK, UTILITY")
	fakePID := flag.Int("pid", 0, "advertise this _NET_WM_PID instead of our own")
	animate := flag.Bool("animate", false, "move a block across the window continuously (changing content for recordings)")
	pattern := flag.Bool("pattern", false, "paint desktop-like busy content (gradients and text-like noise) instead of a solid colour")
	flag.Parse()

	xgb.Logger = log.New(io.Discard, "", 0)
	c, err := xgb.NewConn()
	if err != nil {
		log.Fatal(err)
	}
	setup := xproto.Setup(c)
	screen := setup.DefaultScreen(c)
	wid, _ := xproto.NewWindowId(c)
	pixel, _ := strconv.ParseUint(*color, 16, 32)
	mask := uint32(xproto.EventMaskKeyPress | xproto.EventMaskButtonPress | xproto.EventMaskButtonRelease | xproto.EventMaskExposure | xproto.EventMaskStructureNotify)
	xproto.CreateWindow(c, screen.RootDepth, wid, screen.Root, int16(*x), int16(*y), uint16(*w), uint16(*h), 0,
		xproto.WindowClassInputOutput, screen.RootVisual, xproto.CwBackPixel|xproto.CwEventMask, []uint32{uint32(pixel), mask})

	atom := func(name string) xproto.Atom {
		r, err := xproto.InternAtom(c, false, uint16(len(name)), name).Reply()
		if err != nil {
			log.Fatal(err)
		}
		return r.Atom
	}
	setString := func(prop, typ xproto.Atom, v string) {
		xproto.ChangeProperty(c, xproto.PropModeReplace, wid, prop, typ, 8, uint32(len(v)), []byte(v))
	}
	setString(xproto.AtomWmName, xproto.AtomString, *title)
	setString(atom("_NET_WM_NAME"), atom("UTF8_STRING"), *title)
	setString(xproto.AtomWmClass, xproto.AtomString, "xapp\x00"+*class+"\x00")
	pid := make([]byte, 4)
	advertised := os.Getpid()
	if *fakePID != 0 {
		advertised = *fakePID
	}
	xgb.Put32(pid, uint32(advertised))
	if *winType != "" {
		t := make([]byte, 4)
		xgb.Put32(t, uint32(atom("_NET_WM_WINDOW_TYPE_"+*winType)))
		xproto.ChangeProperty(c, xproto.PropModeReplace, wid, atom("_NET_WM_WINDOW_TYPE"), xproto.AtomAtom, 32, 1, t)
	}
	xproto.ChangeProperty(c, xproto.PropModeReplace, wid, atom("_NET_WM_PID"), xproto.AtomCardinal, 32, 1, pid)
	wmDelete := atom("WM_DELETE_WINDOW")
	protos := make([]byte, 4)
	xgb.Put32(protos, uint32(wmDelete))
	xproto.ChangeProperty(c, xproto.PropModeReplace, wid, atom("WM_PROTOCOLS"), xproto.AtomAtom, 32, 1, protos)
	xproto.MapWindow(c, wid)

	var out *os.File
	if *events != "" {
		out, err = os.OpenFile(*events, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatal(err)
		}
	}
	write := func(e event) {
		if out == nil {
			return
		}
		data, _ := json.Marshal(e)
		out.Write(append(data, '\n'))
	}

	if *animate {
		go animateBlock(wid, *w, *h)
	}
	km := loadKeymap(c, setup)
	for {
		ev, xerr := c.WaitForEvent()
		if ev == nil && xerr == nil {
			return
		}
		switch e := ev.(type) {
		case xproto.ExposeEvent:
			if *pattern && e.Count == 0 {
				paint(c, wid, *w, *h)
			}
		case xproto.MapNotifyEvent:
			if *ready != "" {
				os.WriteFile(*ready, []byte(fmt.Sprint(wid)), 0o644)
			}
		case xproto.ButtonPressEvent:
			write(event{Type: "button", Button: int(e.Detail), X: int(e.RootX), Y: int(e.RootY)})
		case xproto.KeyPressEvent:
			write(event{Type: "key", Keysym: km.keysym(e.Detail, e.State), State: e.State})
		case xproto.MappingNotifyEvent:
			km = loadKeymap(c, setup)
		case xproto.ClientMessageEvent:
			if e.Format == 32 && xproto.Atom(e.Data.Data32[0]) == wmDelete {
				write(event{Type: "close"})
				return
			}
		}
	}
}

type keymap struct {
	min  xproto.Keycode
	per  int
	syms []xproto.Keysym
}

func loadKeymap(c *xgb.Conn, s *xproto.SetupInfo) keymap {
	r, err := xproto.GetKeyboardMapping(c, s.MinKeycode, byte(s.MaxKeycode-s.MinKeycode+1)).Reply()
	if err != nil {
		log.Fatal(err)
	}
	return keymap{min: s.MinKeycode, per: int(r.KeysymsPerKeycode), syms: r.Keysyms}
}

// keysym applies the core protocol rule: Shift selects the second column
// when it is not NoSymbol.
func (k keymap) keysym(kc xproto.Keycode, state uint16) uint32 {
	base := int(kc-k.min) * k.per
	if base < 0 || base+1 >= len(k.syms) {
		return 0
	}
	if state&xproto.ModMaskShift != 0 && k.syms[base+1] != 0 {
		return uint32(k.syms[base+1])
	}
	return uint32(k.syms[base])
}

// paint draws content with the statistics of a busy desktop: horizontal
// colour gradients with "windows" of dark, text-like glyph noise on light
// backgrounds. Rectangles are grouped per colour so each colour costs one
// ChangeGC and a few PolyFillRectangle requests.
func paint(c *xgb.Conn, wid xproto.Window, w, h int) {
	gc, _ := xproto.NewGcontextId(c)
	xproto.CreateGC(c, gc, xproto.Drawable(wid), 0, nil)
	byColor := map[uint32][]xproto.Rectangle{}
	add := func(col uint32, x, y, rw, rh int) {
		byColor[col] = append(byColor[col], xproto.Rectangle{X: int16(x), Y: int16(y), Width: uint16(rw), Height: uint16(rh)})
	}
	for y := 0; y < h; y += 8 {
		for x := 0; x < w; x += 16 {
			r := uint32(x * 255 / max(w, 1))
			g := uint32(y * 255 / max(h, 1))
			add((r&0xf0)<<16|(g&0xf0)<<8|0xa0, x, y, 16, 8)
		}
	}
	seed := uint32(12345)
	rnd := func() uint32 { seed = seed*1664525 + 1013904223; return seed >> 8 }
	for win := 0; win < 6; win++ {
		x0, y0 := int(rnd()%uint32(max(w-640, 1))), int(rnd()%uint32(max(h-420, 1)))
		add(0xf4f4f4, x0, y0, 640, 420)
		for line := y0 + 10; line < y0+410; line += 16 {
			for x := x0 + 8; x < x0+620; x += 3 {
				if rnd()%3 == 0 {
					shade := 0x20 + rnd()%0x60
					add(shade<<16|shade<<8|shade, x, line+int(rnd()%6), 2, 2+int(rnd()%6))
				}
			}
		}
	}
	for col, rects := range byColor {
		xproto.ChangeGC(c, gc, xproto.GcForeground, []uint32{col})
		for len(rects) > 0 {
			n := min(len(rects), 8000)
			xproto.PolyFillRectangle(c, xproto.Drawable(wid), gc, rects[:n])
			rects = rects[n:]
		}
	}
	xproto.GetInputFocus(c).Reply() // flush
}

// animateBlock redraws a moving block about 30 times a second over a
// gradient background on its own connection.
func animateBlock(wid xproto.Window, w, h int) {
	c, err := xgb.NewConn()
	if err != nil {
		return
	}
	gc, _ := xproto.NewGcontextId(c)
	xproto.CreateGC(c, gc, xproto.Drawable(wid), 0, nil)
	for frame := 0; ; frame++ {
		x := (frame * 12) % max(w-120, 1)
		y := (frame * 7) % max(h-120, 1)
		xproto.ChangeGC(c, gc, xproto.GcForeground, []uint32{uint32(frame*2654435761) & 0xffffff})
		xproto.PolyFillRectangle(c, xproto.Drawable(wid), gc, []xproto.Rectangle{{X: int16(x), Y: int16(y), Width: 120, Height: 120}})
		xproto.GetInputFocus(c).Reply()
		time.Sleep(33 * time.Millisecond)
	}
}
