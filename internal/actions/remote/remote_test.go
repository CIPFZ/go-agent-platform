package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/CIPFZ/go-agent-platform/internal/actions/clipboard"
	"github.com/CIPFZ/go-agent-platform/internal/actions/input"
	"github.com/CIPFZ/go-agent-platform/internal/actions/screen"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// fakeInput records what reaches the input backend.
type fakeInput struct {
	mu  sync.Mutex
	log []string
}

func (f *fakeInput) add(format string, args ...any) {
	f.mu.Lock()
	f.log = append(f.log, fmt.Sprintf(format, args...))
	f.mu.Unlock()
}

func (f *fakeInput) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func (f *fakeInput) Move(x, y int) error                 { f.add("move %d,%d", x, y); return nil }
func (f *fakeInput) Position() (int, int, error)         { return 0, 0, nil }
func (f *fakeInput) Button(b input.Button, d bool) error { f.add("button %s %v", b, d); return nil }
func (f *fakeInput) Scroll(dx, dy int) error             { f.add("scroll %d,%d", dx, dy); return nil }
func (f *fakeInput) Key(k input.Key, d bool) error       { f.add("key %s %v", k.Name, d); return nil }
func (f *fakeInput) TypeUnicode(r rune) error            { f.add("type %c", r); return nil }
func (f *fakeInput) TypeScancode(r rune) error           { f.add("scan %c", r); return nil }
func (f *fakeInput) Begin() error                        { return nil }
func (f *fakeInput) End()                                {}

type fakeScreen struct{}

func (fakeScreen) Displays() ([]screen.Display, error) {
	return []screen.Display{{Index: 0, Bounds: protocol.Rect{W: 1920, H: 1080}, Scale: 1, Primary: true}}, nil
}
func (fakeScreen) Capture(protocol.Rect) (*image.RGBA, error) { return nil, errors.New("not used") }
func (fakeScreen) Cursor() (screen.Cursor, error) {
	return screen.Cursor{X: 5, Y: 6, Visible: true}, nil
}
func (fakeScreen) CursorImage() (*screen.CursorImage, error) {
	return &screen.CursorImage{Image: image.NewRGBA(image.Rect(0, 0, 8, 8)), Hotspot: image.Pt(1, 2)}, nil
}

// fakeSource emits a small RTP stream at the requested frame rate.
type fakeSource struct{ runs chan Params }

func (fakeSource) Available() error { return nil }

func (f fakeSource) Run(ctx context.Context, p Params, emit func(*rtp.Packet), status func(Status)) error {
	select {
	case f.runs <- p:
	default:
	}
	status(Status{Capture: "fake", Encoder: "none"})
	t := time.NewTicker(time.Second / time.Duration(p.FPS))
	defer t.Stop()
	var seq uint16
	var ts uint32
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		seq++
		ts += 3000
		// A single-NAL-unit packet holding a non-IDR slice header.
		emit(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: 7, Marker: true},
			Payload: []byte{0x41, 0x9a, 0x00, 0x01}})
	}
}

type viewer struct {
	t       *testing.T
	pc      *webrtc.PeerConnection
	input   *webrtc.DataChannel
	pointer *webrtc.DataChannel
	control *webrtc.DataChannel
	offer   string
	packets chan *rtp.Packet
	msgs    chan map[string]any
	open    chan struct{}
}

func newViewer(t *testing.T) *viewer {
	t.Helper()
	var se webrtc.SettingEngine
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetIncludeLoopbackCandidate(true)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	v := &viewer{t: t, pc: pc, packets: make(chan *rtp.Packet, 256), msgs: make(chan map[string]any, 64), open: make(chan struct{})}
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			pkt, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			select {
			case v.packets <- pkt:
			default:
			}
		}
	})
	unordered, noRetransmit := false, uint16(0)
	if v.input, err = pc.CreateDataChannel("input", nil); err != nil {
		t.Fatal(err)
	}
	if v.pointer, err = pc.CreateDataChannel("pointer", &webrtc.DataChannelInit{Ordered: &unordered, MaxRetransmits: &noRetransmit}); err != nil {
		t.Fatal(err)
	}
	if v.control, err = pc.CreateDataChannel("control", nil); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	v.input.OnOpen(func() { once.Do(func() { close(v.open) }) })
	v.control.OnMessage(func(m webrtc.DataChannelMessage) {
		var msg map[string]any
		if json.Unmarshal(m.Data, &msg) == nil {
			select {
			case v.msgs <- msg:
			default:
			}
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-done
	v.offer = pc.LocalDescription().SDP
	return v
}

func (v *viewer) answer(sdp string) {
	v.t.Helper()
	if err := v.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
		v.t.Fatal(err)
	}
}

func (v *viewer) send(dc *webrtc.DataChannel, msg string) {
	v.t.Helper()
	if err := dc.SendText(msg); err != nil {
		v.t.Fatal(err)
	}
}

// message waits for a control message of type typ.
func (v *viewer) message(typ string) map[string]any {
	v.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case m := <-v.msgs:
			if m["t"] == typ {
				return m
			}
		case <-deadline:
			v.t.Fatalf("no %q control message", typ)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func contains(calls []string, want ...string) bool {
	i := 0
	for _, c := range calls {
		if i < len(want) && c == want[i] {
			i++
		}
	}
	return i == len(want)
}

type env struct {
	m      *Manager
	in     *fakeInput
	events chan string
	runs   chan Params
}

func newEnv(t *testing.T, idle time.Duration) *env {
	t.Helper()
	e := &env{in: &fakeInput{}, events: make(chan string, 16), runs: make(chan Params, 4)}
	e.m = New(Options{Input: input.NewService(e.in), Screen: screen.NewService(fakeScreen{}), Source: fakeSource{runs: e.runs},
		Loopback: true, IdleRelease: idle,
		OnEvent: func(kind string, d map[string]string) { e.events <- kind + ":" + d["reason"] }})
	t.Cleanup(e.m.Shutdown)
	return e
}

func (e *env) open(t *testing.T, v *viewer, holder, client string) *OpenResult {
	t.Helper()
	res, err := e.m.Open(context.Background(), OpenRequest{SDP: v.offer, Holder: holder, ClientID: client, RemoteAddr: "10.0.0.9:1"})
	if err != nil {
		t.Fatal(err)
	}
	v.answer(res.SDP)
	return res
}

func TestSessionStreamsVideoAndAppliesInput(t *testing.T) {
	e := newEnv(t, 0)
	v := newViewer(t)
	res := e.open(t, v, "alice", "tab-1")
	if res.Display.W != 1920 || res.FPS != DefaultFPS || res.BitrateKbps != 1920*1080/512 || !strings.HasPrefix(res.SessionID, "rd-") {
		t.Fatalf("result = %+v", res)
	}

	// Video: packets arrive as one increasing sequence.
	var prev *rtp.Packet
	for i := 0; i < 10; i++ {
		select {
		case pkt := <-v.packets:
			if prev != nil && pkt.SequenceNumber != prev.SequenceNumber+1 {
				t.Fatalf("sequence %d after %d", pkt.SequenceNumber, prev.SequenceNumber)
			}
			prev = pkt
		case <-time.After(10 * time.Second):
			t.Fatal("no video")
		}
	}
	if p := <-e.runs; p.FPS != DefaultFPS || p.Display.W != 1920 {
		t.Fatalf("source params = %+v", p)
	}

	// Control: greeting, pipeline status and the pointer shape.
	if m := v.message("hello"); m["session_id"] != res.SessionID {
		t.Fatalf("hello = %v", m)
	}
	if m := v.message("cursor"); m["w"] != float64(8) || m["hx"] != float64(1) || m["png"] == "" {
		t.Fatalf("cursor = %v", m)
	}

	// Input: every kind of event reaches the backend in order; positions are
	// clamped to the display.
	<-v.open
	v.send(v.input, `{"t":"md","b":0,"x":100,"y":200}`)
	v.send(v.input, `{"t":"mu","b":0,"x":5000,"y":-3}`)
	v.send(v.input, `{"t":"kd","c":"ShiftLeft"}`)
	v.send(v.input, `{"t":"kd","c":"KeyA"}`)
	v.send(v.input, `{"t":"ku","c":"KeyA"}`)
	v.send(v.input, `{"t":"ku","c":"ShiftLeft"}`)
	v.send(v.input, `{"t":"mw","dy":100}`)
	v.send(v.input, `{"t":"tx","s":"中"}`)
	v.send(v.input, `{"t":"kd","c":"NoSuchKey"}`)
	want := []string{"move 100,200", "button left true", "move 1919,0", "button left false", "key shift true", "key a true",
		"key a false", "key shift false", "scroll 0,-1", "type 中"}
	waitFor(t, "input", func() bool { return contains(e.in.calls(), want...) })

	info := e.m.Sessions()
	if len(info) != 1 || info[0].Holder != "alice" || info[0].State != "connected" || info[0].Capture != "fake" ||
		info[0].KeyEvents != 6 || info[0].MouseEvents != 3 || info[0].LastInputAt == nil || info[0].FramesSent == 0 {
		t.Fatalf("sessions = %+v", info)
	}

	// The viewer's statistics are kept for the session list.
	v.send(v.control, `{"t":"stats","stats":{"fps":29.5}}`)
	waitFor(t, "client stats", func() bool { s := e.m.Sessions(); return len(s) == 1 && s[0].Client["fps"] == 29.5 })
}

func TestDesktopIsLeasedToOnePerson(t *testing.T) {
	e := newEnv(t, 0)
	v := newViewer(t)
	first := e.open(t, v, "alice", "tab-1")
	if err := e.m.InUse(); err == nil {
		t.Fatal("InUse = nil while a session is open")
	}

	// Someone else is refused and told who holds the desktop.
	_, err := e.m.Open(context.Background(), OpenRequest{SDP: newViewer(t).offer, Holder: "bob", ClientID: "tab-2"})
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeConflict || perr.Details["reason"] != protocol.ReasonRemoteInUse || perr.Details["holder"] != "alice" {
		t.Fatalf("second open = %v", err)
	}

	// Keys still down when the session ends are released.
	<-v.open
	v.send(v.input, `{"t":"kd","c":"ControlLeft"}`)
	v.send(v.input, `{"t":"md","b":2,"x":1,"y":1}`)
	waitFor(t, "held input", func() bool { return contains(e.in.calls(), "key ctrl true", "button right true") })

	// The same tab reconnecting takes over its own session.
	v2 := newViewer(t)
	second := e.open(t, v2, "alice", "tab-1")
	if second.SessionID == first.SessionID {
		t.Fatal("reconnect reused the session id")
	}
	waitFor(t, "release of held input", func() bool {
		return contains(e.in.calls(), "key ctrl false") && contains(e.in.calls(), "button right false")
	})
	if got := <-e.events; got != "remote_open:" {
		t.Fatalf("event = %q", got)
	}
	if got := <-e.events; got != "remote_close:replaced" {
		t.Fatalf("event = %q", got)
	}

	// Releasing frees the desktop for the next person.
	if err := e.m.Close("rd-unknown"); err == nil {
		t.Fatal("closing an unknown session succeeded")
	}
	if err := e.m.Close(second.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := e.m.InUse(); err != nil {
		t.Fatalf("InUse after release = %v", err)
	}
	if len(e.m.Sessions()) != 0 {
		t.Fatal("session still listed")
	}
	e.open(t, newViewer(t), "bob", "tab-2")
}

func TestIdleSessionIsReleased(t *testing.T) {
	e := newEnv(t, 1500*time.Millisecond)
	v := newViewer(t)
	e.open(t, v, "alice", "tab-1")
	// The audit event is the outcome; the page's "bye" is only delivered if
	// its connection came up before the session ended, which a slow machine
	// does not guarantee within the idle time used here.
	if got := <-e.events; got != "remote_open:" {
		t.Fatalf("event = %q", got)
	}
	select {
	case got := <-e.events:
		if got != "remote_close:idle" {
			t.Fatalf("event = %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the idle session was not released")
	}
	waitFor(t, "release", func() bool { return e.m.InUse() == nil })
}

func TestOpenValidates(t *testing.T) {
	e := newEnv(t, 0)
	for name, r := range map[string]OpenRequest{
		"no sdp":      {Holder: "a"},
		"no holder":   {SDP: "x"},
		"fps":         {SDP: "x", Holder: "a", FPS: 500},
		"bitrate":     {SDP: "x", Holder: "a", BitrateKbps: 1},
		"display":     {SDP: "x", Holder: "a", Display: 1},
		"garbage sdp": {SDP: "x", Holder: "a"},
	} {
		_, err := e.m.Open(context.Background(), r)
		var perr *protocol.Error
		if !errors.As(err, &perr) || perr.Code != protocol.CodeInvalidRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := e.m.InUse(); err != nil {
		t.Fatalf("a failed open left the desktop taken: %v", err)
	}
}

func TestCodeKeysResolve(t *testing.T) {
	for code, name := range codeKeys {
		if _, ok := keyForCode(code); !ok {
			t.Errorf("%s maps to %q, which is not a key of docs/keys.md", code, name)
		}
	}
}

// fakeClipboard is a clipboard whose content and sequence number a test sets.
type fakeClipboard struct {
	mu   sync.Mutex
	seq  uint32
	text string
}

func (f *fakeClipboard) set(text string) {
	f.mu.Lock()
	f.text = text
	f.seq++
	f.mu.Unlock()
}

func (f *fakeClipboard) Sequence() uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seq
}

func (f *fakeClipboard) Read(context.Context) (clipboard.Content, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	text := f.text
	return clipboard.Content{Formats: []string{"CF_UNICODETEXT"}, Text: &text}, nil
}

func (f *fakeClipboard) WriteText(_ context.Context, text string) error { f.set(text); return nil }
func (f *fakeClipboard) WritePNG(context.Context, []byte) error         { return nil }

func TestClipboardAndLockStateReachThePage(t *testing.T) {
	cb := &fakeClipboard{text: "from before the session"}
	var locked atomic.Bool
	in := &fakeInput{}
	m := New(Options{Input: input.NewService(in), Screen: screen.NewService(fakeScreen{}), Source: fakeSource{runs: make(chan Params, 4)},
		Loopback: true, Clipboard: clipboard.NewService(cb), Desktop: func() Desktop { return Desktop{Locked: locked.Load()} }})
	t.Cleanup(m.Shutdown)
	v := newViewer(t)
	res, err := m.Open(context.Background(), OpenRequest{SDP: v.offer, Holder: "alice", ClientID: "tab-1"})
	if err != nil {
		t.Fatal(err)
	}
	v.answer(res.SDP)
	v.message("hello")

	// Only what is copied during the session is sent, and long text is
	// announced instead of carried.
	time.Sleep(700 * time.Millisecond)
	cb.set("copied on the desktop")
	if m := v.message("clip"); m["text"] != "copied on the desktop" {
		t.Fatalf("clip = %v", m)
	}
	cb.set(strings.Repeat("x", MaxClipboardText+1))
	if m := v.message("clip"); m["large"] != true || m["text"] != nil {
		t.Fatalf("large clip = %v", m)
	}

	locked.Store(true)
	if m := v.message("desk"); m["locked"] != true {
		t.Fatalf("desk = %v", m)
	}
	locked.Store(false)
	if m := v.message("desk"); m["locked"] != false {
		t.Fatalf("desk = %v", m)
	}
	if m.InUse() == nil {
		t.Fatal("locking the desktop ended the session")
	}
}

// fakeVirtualDisplay stands in for the driver: the compositor is active
// while a real or a virtual monitor is present.
type fakeVirtualDisplay struct {
	mu      sync.Mutex
	real    bool
	plugged int
	plugs   []string
}

func (f *fakeVirtualDisplay) Installed() bool { return true }

func (f *fakeVirtualDisplay) Active() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.real || f.plugged > 0
}

func (f *fakeVirtualDisplay) Plugged() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plugged
}

func (f *fakeVirtualDisplay) RealDisplay() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.real
}

func (f *fakeVirtualDisplay) Plug(size protocol.Rect, scale float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plugged++
	f.plugs = append(f.plugs, fmt.Sprintf("%dx%d@%.2f", size.W, size.H, scale))
	return nil
}

func (f *fakeVirtualDisplay) Unplug() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.plugged > 0 {
		f.plugged--
	}
	return nil
}

func (f *fakeVirtualDisplay) setReal(v bool) {
	f.mu.Lock()
	f.real = v
	f.mu.Unlock()
}

func TestVirtualDisplayFollowsTheSession(t *testing.T) {
	vd := &fakeVirtualDisplay{}
	dir := t.TempDir()
	newManager := func() *Manager {
		return New(Options{Input: input.NewService(&fakeInput{}), Screen: screen.NewService(fakeScreen{}), Source: fakeSource{runs: make(chan Params, 4)},
			Loopback: true, VirtualDisplay: vd, StateDir: dir})
	}
	m := newManager()
	open := func(client string) *OpenResult {
		v := newViewer(t)
		res, err := m.Open(context.Background(), OpenRequest{SDP: v.offer, Holder: "alice", ClientID: client})
		if err != nil {
			t.Fatal(err)
		}
		v.answer(res.SDP)
		return res
	}

	// No display is active: the session brings its own, sized like the desktop.
	pv := newViewer(t)
	pres, err := m.Open(context.Background(), OpenRequest{SDP: pv.offer, Holder: "alice", ClientID: "tab-0"})
	if err != nil {
		t.Fatal(err)
	}
	pv.answer(pres.SDP)
	// The virtual monitor carries the pointer in its picture; the page is told.
	if msg := pv.message("pointer"); msg["embedded"] != true {
		t.Fatalf("pointer = %v", msg)
	}
	if err := m.Close(pres.SessionID); err != nil {
		t.Fatal(err)
	}
	vd.plugs = nil
	first := open("tab-1")
	if vd.Plugged() != 1 || len(vd.plugs) != 1 || vd.plugs[0] != "1920x1080@1.00" || !markerExists(dir) {
		t.Fatalf("after open: plugged %d %v marker %v", vd.Plugged(), vd.plugs, markerExists(dir))
	}
	// The same tab reconnecting keeps it.
	second := open("tab-1")
	if second.SessionID == first.SessionID || vd.Plugged() != 1 || len(vd.plugs) != 1 {
		t.Fatalf("after reconnect: plugged %d %v", vd.Plugged(), vd.plugs)
	}
	// The lid is opened: the desktop goes back to the panel.
	m.vd.mu.Lock()
	m.vd.changed = time.Time{}
	m.vd.mu.Unlock()
	vd.setReal(true)
	waitFor(t, "unplug when a real display returns", func() bool { return vd.Plugged() == 0 })
	// The lid is closed again during the session.
	m.vd.mu.Lock()
	m.vd.changed = time.Time{}
	m.vd.mu.Unlock()
	vd.setReal(false)
	waitFor(t, "plug when the display goes away", func() bool { return vd.Plugged() == 1 })
	// The session ends: the monitor goes with it.
	if err := m.Close(second.SessionID); err != nil {
		t.Fatal(err)
	}
	if vd.Plugged() != 0 || markerExists(dir) {
		t.Fatalf("after close: plugged %d marker %v", vd.Plugged(), markerExists(dir))
	}
	// With a real display nothing is added.
	vd.setReal(true)
	third := open("tab-1")
	if vd.Plugged() != 0 {
		t.Fatal("a virtual monitor was added although a display is active")
	}
	_ = m.Close(third.SessionID)
	m.Shutdown()

	// The real display's size and scale were noted while it was on (the
	// fake screen reports 1920x1080 at 100 %); whatever the desktop claims
	// later, the virtual monitor gets those.
	if r, ok := loadDisplayRecord(dir); !ok || r != (displayRecord{W: 1920, H: 1080, Scale: 1}) {
		t.Fatalf("display record = %+v %v", r, ok)
	}
	saveDisplayRecord(dir, displayRecord{W: 2560, H: 1600, Scale: 1.5})
	vd.setReal(false)
	m = newManager()
	fourth := open("tab-1")
	if got := vd.plugs[len(vd.plugs)-1]; got != "2560x1600@1.50" {
		t.Fatalf("virtual monitor geometry = %s", got)
	}
	_ = m.Close(fourth.SessionID)
	m.Shutdown()

	// A run that died with its monitor connected is cleaned up by the next;
	// a monitor without the marker belongs to someone else and stays.
	vd.setReal(false)
	vd.plugged = 1
	writeMarker(dir)
	m = newManager()
	if vd.Plugged() != 0 || markerExists(dir) {
		t.Fatalf("after restart: plugged %d marker %v", vd.Plugged(), markerExists(dir))
	}
	m.Shutdown()
	vd.plugged = 1
	m = newManager()
	defer m.Shutdown()
	if vd.Plugged() != 1 {
		t.Fatal("a monitor this process did not add was removed")
	}
}
