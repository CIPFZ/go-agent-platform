//go:build linux

package clipboard

import (
	"context"
	"sync"
	"unicode/utf8"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/CIPFZ/go-agent-platform/internal/platform/x11"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func newBackend() Backend { return &selectionBackend{} }

// selectionBackend owns and requests the X CLIPBOARD selection in pure Go
// (spec section 10). It uses its own X connection and an unmapped window,
// because selection ownership and requests are event driven. Content it
// owns lives in this process: when vm-agent exits the clipboard is empty
// unless a clipboard manager has copied it.
type selectionBackend struct {
	mu   sync.Mutex // serialises clipboard operations
	conn *selConn
}

// incrChunk is the size of one INCR transfer step; it keeps ChangeProperty
// requests well below the core protocol's 256 KiB request limit.
const incrChunk = 128 << 10

// selConn is one connection with its window, atoms and event dispatch.
type selConn struct {
	c     *x11.Conn
	win   xproto.Window
	atoms map[string]xproto.Atom
	// events to the requesting side (SelectionNotify, PropertyNotify on
	// our window); the owner side is handled on the event goroutine.
	events chan xgb.Event

	mu       sync.Mutex // guards the owned content and transfers
	owned    *owned
	incrSend map[[2]uint32]*incrTransfer
}

type owned struct {
	targets map[xproto.Atom][]byte // target -> data
	time    xproto.Timestamp
}

type incrTransfer struct {
	requestor xproto.Window
	property  xproto.Atom
	target    xproto.Atom
	data      []byte
	offset    int
	finished  bool
}

var atomNames = []string{"CLIPBOARD", "TARGETS", "TIMESTAMP", "MULTIPLE", "INCR", "UTF8_STRING", "STRING", "TEXT",
	"text/plain;charset=utf-8", "text/plain", "image/png", "VM_AGENT_SELECTION", "VM_AGENT_TIMESTAMP", "ATOM", "INTEGER"}

func (b *selectionBackend) connect() (*selConn, error) {
	if b.conn != nil && b.conn.c.Alive() {
		return b.conn, nil
	}
	sc := &selConn{events: make(chan xgb.Event, 256), atoms: map[string]xproto.Atom{}, incrSend: map[[2]uint32]*incrTransfer{}}
	c, err := x11.Dial(sc.handle)
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeNoDisplay, "%v", err)
	}
	sc.c = c
	for _, name := range atomNames {
		a, err := c.Atom(name)
		if err != nil {
			c.Close()
			return nil, err
		}
		sc.atoms[name] = a
	}
	win, err := xproto.NewWindowId(c.X)
	if err != nil {
		c.Close()
		return nil, err
	}
	if err := xproto.CreateWindowChecked(c.X, 0, win, c.Root, -10, -10, 1, 1, 0, xproto.WindowClassInputOnly,
		c.Screen.RootVisual, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange}).Check(); err != nil {
		c.Close()
		return nil, c.Check(err)
	}
	sc.win = win
	b.conn = sc
	return sc, nil
}

func (sc *selConn) a(name string) xproto.Atom { return sc.atoms[name] }

// handle runs on the connection's event goroutine.
func (sc *selConn) handle(ev xgb.Event) {
	switch e := ev.(type) {
	case xproto.SelectionRequestEvent:
		sc.serve(e)
	case xproto.SelectionClearEvent:
		if e.Selection == sc.a("CLIPBOARD") {
			sc.mu.Lock()
			if sc.owned != nil && e.Time >= sc.owned.time {
				sc.owned = nil
			}
			sc.mu.Unlock()
		}
	case xproto.PropertyNotifyEvent:
		if e.Window != sc.win {
			sc.continueIncr(e)
			return
		}
		sc.forward(ev)
	case xproto.SelectionNotifyEvent:
		sc.forward(ev)
	}
}

func (sc *selConn) forward(ev xgb.Event) {
	select {
	case sc.events <- ev:
	default: // a stale event nobody waits for; drop rather than block
	}
}

// serve answers a SelectionRequest from another client (ICCCM 2.2).
func (sc *selConn) serve(e xproto.SelectionRequestEvent) {
	property := e.Property
	if property == 0 {
		property = e.Target // obsolete clients
	}
	sc.mu.Lock()
	o := sc.owned
	sc.mu.Unlock()
	ok := false
	if o != nil && e.Selection == sc.a("CLIPBOARD") && e.Owner == sc.win {
		switch e.Target {
		case sc.a("TARGETS"):
			targets := []xproto.Atom{sc.a("TARGETS"), sc.a("TIMESTAMP")}
			for t := range o.targets {
				targets = append(targets, t)
			}
			data := make([]byte, 4*len(targets))
			for i, t := range targets {
				xgb.Put32(data[i*4:], uint32(t))
			}
			ok = xproto.ChangePropertyChecked(sc.c.X, xproto.PropModeReplace, e.Requestor, property, sc.a("ATOM"), 32, uint32(len(targets)), data).Check() == nil
		case sc.a("TIMESTAMP"):
			data := make([]byte, 4)
			xgb.Put32(data, uint32(o.time))
			ok = xproto.ChangePropertyChecked(sc.c.X, xproto.PropModeReplace, e.Requestor, property, sc.a("INTEGER"), 32, 1, data).Check() == nil
		default:
			if data, has := o.targets[e.Target]; has {
				ok = sc.send(e.Requestor, property, e.Target, data)
			}
		}
	}
	if !ok {
		property = 0 // refusal
	}
	notify := xproto.SelectionNotifyEvent{Time: e.Time, Requestor: e.Requestor, Selection: e.Selection, Target: e.Target, Property: property}
	xproto.SendEvent(sc.c.X, false, e.Requestor, xproto.EventMaskNoEvent, string(notify.Bytes()))
}

// send stores data in the requestor's property, starting an INCR transfer
// when it exceeds one chunk.
func (sc *selConn) send(requestor xproto.Window, property, target xproto.Atom, data []byte) bool {
	if len(data) <= incrChunk {
		return xproto.ChangePropertyChecked(sc.c.X, xproto.PropModeReplace, requestor, property, target, 8, uint32(len(data)), data).Check() == nil
	}
	// Watch the requestor's property deletions on our connection only.
	if xproto.ChangeWindowAttributesChecked(sc.c.X, requestor, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange}).Check() != nil {
		return false
	}
	size := make([]byte, 4)
	xgb.Put32(size, uint32(len(data)))
	if xproto.ChangePropertyChecked(sc.c.X, xproto.PropModeReplace, requestor, property, sc.a("INCR"), 32, 1, size).Check() != nil {
		return false
	}
	sc.mu.Lock()
	sc.incrSend[[2]uint32{uint32(requestor), uint32(property)}] = &incrTransfer{requestor: requestor, property: property, target: target, data: data}
	sc.mu.Unlock()
	return true
}

// continueIncr sends the next INCR chunk when the requestor deleted the
// previous one; a zero-length chunk ends the transfer.
func (sc *selConn) continueIncr(e xproto.PropertyNotifyEvent) {
	if e.State != xproto.PropertyDelete {
		return
	}
	key := [2]uint32{uint32(e.Window), uint32(e.Atom)}
	sc.mu.Lock()
	t := sc.incrSend[key]
	if t != nil && t.finished {
		delete(sc.incrSend, key)
	}
	sc.mu.Unlock()
	if t == nil {
		return
	}
	if t.finished {
		xproto.ChangeWindowAttributes(sc.c.X, t.requestor, xproto.CwEventMask, []uint32{xproto.EventMaskNoEvent})
		return
	}
	end := min(t.offset+incrChunk, len(t.data))
	chunk := t.data[t.offset:end]
	t.offset = end
	if len(chunk) == 0 {
		t.finished = true
	}
	xproto.ChangeProperty(sc.c.X, xproto.PropModeReplace, t.requestor, t.property, t.target, 8, uint32(len(chunk)), chunk)
}

// timestamp obtains a server timestamp from a zero-length property append
// (ICCCM 2.1: ownership must not use CurrentTime).
func (sc *selConn) timestamp(ctx context.Context) (xproto.Timestamp, error) {
	sc.drain()
	prop := sc.a("VM_AGENT_TIMESTAMP")
	if err := xproto.ChangePropertyChecked(sc.c.X, xproto.PropModeAppend, sc.win, prop, sc.a("INTEGER"), 32, 0, nil).Check(); err != nil {
		return 0, sc.c.Check(err)
	}
	for {
		ev, err := sc.next(ctx)
		if err != nil {
			return 0, err
		}
		if p, ok := ev.(xproto.PropertyNotifyEvent); ok && p.Atom == prop {
			return p.Time, nil
		}
	}
}

func (sc *selConn) drain() {
	for {
		select {
		case <-sc.events:
		default:
			return
		}
	}
}

func (sc *selConn) next(ctx context.Context) (xgb.Event, error) {
	select {
	case ev := <-sc.events:
		return ev, nil
	case <-sc.c.Dead():
		return nil, protocol.Errorf(protocol.CodeNoDisplay, "the X connection was lost")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *selectionBackend) own(ctx context.Context, targets map[string][]byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	sc, err := b.connect()
	if err != nil {
		return err
	}
	ts, err := sc.timestamp(ctx)
	if err != nil {
		return err
	}
	o := &owned{targets: map[xproto.Atom][]byte{}, time: ts}
	for name, data := range targets {
		o.targets[sc.a(name)] = data
	}
	sc.mu.Lock()
	sc.owned = o
	sc.mu.Unlock()
	if err := xproto.SetSelectionOwnerChecked(sc.c.X, sc.win, sc.a("CLIPBOARD"), ts).Check(); err != nil {
		return sc.c.Check(err)
	}
	reply, err := xproto.GetSelectionOwner(sc.c.X, sc.a("CLIPBOARD")).Reply()
	if err != nil {
		return sc.c.Check(err)
	}
	if reply.Owner != sc.win {
		return protocol.Errorf(protocol.CodeIOFailed, "another client kept the clipboard")
	}
	return nil
}

func (b *selectionBackend) WriteText(ctx context.Context, text string) error {
	if !utf8.ValidString(text) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "text must be valid UTF-8")
	}
	latin1 := make([]byte, 0, len(text))
	for _, r := range text {
		if r > 0xff {
			r = '?'
		}
		latin1 = append(latin1, byte(r))
	}
	utf := []byte(text)
	return b.own(ctx, map[string][]byte{"UTF8_STRING": utf, "text/plain;charset=utf-8": utf, "TEXT": utf, "STRING": latin1, "text/plain": latin1})
}

func (b *selectionBackend) WritePNG(ctx context.Context, data []byte) error {
	return b.own(ctx, map[string][]byte{"image/png": data})
}

// metaTargets are protocol targets, not content formats.
var metaTargets = map[string]bool{"TARGETS": true, "TIMESTAMP": true, "MULTIPLE": true, "SAVE_TARGETS": true, "DELETE": true, "INSERT_SELECTION": true, "INSERT_PROPERTY": true}

func (b *selectionBackend) Read(ctx context.Context) (Content, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sc, err := b.connect()
	if err != nil {
		return Content{}, err
	}
	targetsData, typ, err := sc.convert(ctx, sc.a("TARGETS"))
	if err != nil {
		return Content{}, err
	}
	content := Content{Formats: []string{}}
	if typ != sc.a("ATOM") && typ != sc.a("TARGETS") {
		return content, nil // no owner, or the owner refused
	}
	have := map[string]bool{}
	for i := 0; i+4 <= len(targetsData); i += 4 {
		name, err := sc.c.AtomName(xproto.Atom(xgb.Get32(targetsData[i:])))
		if err != nil || metaTargets[name] || have[name] {
			continue
		}
		have[name] = true
		content.Formats = append(content.Formats, name)
	}
	for _, t := range []string{"UTF8_STRING", "text/plain;charset=utf-8", "STRING", "TEXT", "text/plain"} {
		if !have[t] {
			continue
		}
		data, typ, err := sc.convert(ctx, sc.a(t))
		if err != nil {
			return Content{}, err
		}
		if typ == 0 {
			continue
		}
		var text string
		if t == "STRING" || (t == "text/plain" && !utf8.Valid(data)) {
			runes := make([]rune, len(data))
			for i, c := range data {
				runes[i] = rune(c)
			}
			text = string(runes)
		} else {
			text = string(data)
		}
		content.Text = &text
		break
	}
	if have["image/png"] {
		data, typ, err := sc.convert(ctx, sc.a("image/png"))
		if err != nil {
			return Content{}, err
		}
		if typ != 0 {
			content.PNG = data
		}
	}
	return content, nil
}

// convert requests the CLIPBOARD selection as target and returns the data
// and its type; type 0 means the request was refused or nobody owns the
// selection. INCR transfers are reassembled.
func (sc *selConn) convert(ctx context.Context, target xproto.Atom) ([]byte, xproto.Atom, error) {
	// When vm-agent itself owns the clipboard, answer from memory: the
	// request would otherwise be served by the event goroutine we wait on.
	sc.mu.Lock()
	o := sc.owned
	sc.mu.Unlock()
	if o != nil {
		if target == sc.a("TARGETS") {
			out := make([]byte, 4*len(o.targets))
			i := 0
			for t := range o.targets {
				xgb.Put32(out[i*4:], uint32(t))
				i++
			}
			return out, sc.a("ATOM"), nil
		}
		if data, ok := o.targets[target]; ok {
			return data, target, nil
		}
		return nil, 0, nil
	}
	sc.drain()
	prop := sc.a("VM_AGENT_SELECTION")
	_ = xproto.DeletePropertyChecked(sc.c.X, sc.win, prop).Check()
	if err := xproto.ConvertSelectionChecked(sc.c.X, sc.win, sc.a("CLIPBOARD"), target, prop, xproto.TimeCurrentTime).Check(); err != nil {
		return nil, 0, sc.c.Check(err)
	}
	for {
		ev, err := sc.next(ctx)
		if err != nil {
			return nil, 0, err
		}
		n, ok := ev.(xproto.SelectionNotifyEvent)
		if !ok || n.Requestor != sc.win || n.Target != target {
			continue
		}
		if n.Property == 0 {
			return nil, 0, nil
		}
		break
	}
	reply, err := xproto.GetProperty(sc.c.X, true, sc.win, prop, xproto.GetPropertyTypeAny, 0, MaxBytes/4+1).Reply()
	if err != nil {
		return nil, 0, sc.c.Check(err)
	}
	if reply.Type != sc.a("INCR") {
		if int(reply.ValueLen)*int(reply.Format/8) > MaxBytes {
			return nil, 0, protocol.Errorf(protocol.CodeTooLarge, "clipboard data exceeds %d bytes", MaxBytes)
		}
		return reply.Value, reply.Type, nil
	}
	// INCR: the delete above asked for the first chunk; each chunk arrives
	// as a new property value and is acknowledged by deleting it.
	var out []byte
	var typ xproto.Atom
	for {
		ev, err := sc.next(ctx)
		if err != nil {
			return nil, 0, err
		}
		p, ok := ev.(xproto.PropertyNotifyEvent)
		if !ok || p.Atom != prop || p.State != xproto.PropertyNewValue {
			continue
		}
		chunk, err := xproto.GetProperty(sc.c.X, true, sc.win, prop, xproto.GetPropertyTypeAny, 0, MaxBytes/4+1).Reply()
		if err != nil {
			return nil, 0, sc.c.Check(err)
		}
		if len(chunk.Value) == 0 {
			return out, typ, nil
		}
		typ = chunk.Type
		out = append(out, chunk.Value...)
		if len(out) > MaxBytes {
			return nil, 0, protocol.Errorf(protocol.CodeTooLarge, "clipboard data exceeds %d bytes", MaxBytes)
		}
	}
}
