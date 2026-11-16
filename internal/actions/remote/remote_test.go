package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

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
	if m := v.message("bye"); m["reason"] != "idle" {
		t.Fatalf("bye = %v", m)
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
