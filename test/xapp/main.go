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
	xgb.Put32(pid, uint32(os.Getpid()))
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

	km := loadKeymap(c, setup)
	for {
		ev, xerr := c.WaitForEvent()
		if ev == nil && xerr == nil {
			return
		}
		switch e := ev.(type) {
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
