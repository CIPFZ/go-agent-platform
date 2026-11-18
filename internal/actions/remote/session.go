package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"hash/fnv"
	"image/png"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/CIPFZ/go-agent-platform/internal/actions/input"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Wire limits of the data channels.
const (
	maxMessage   = 16 << 10
	maxText      = 4096
	eventBacklog = 512
	// wheelPerPixel converts a browser wheel delta in CSS pixels (100 per
	// mouse notch) to wheel delta units (120 per notch).
	wheelPerPixel = 1.2
)

// message is one data channel message from the page. T is the type:
// kd/ku key down/up (C is KeyboardEvent.code), mm move, md/mu button
// down/up (B is MouseEvent.button), mw wheel, tx text, rel release all.
type message struct {
	T  string  `json:"t"`
	C  string  `json:"c,omitempty"`
	X  *int    `json:"x,omitempty"`
	Y  *int    `json:"y,omitempty"`
	B  int     `json:"b,omitempty"`
	DX float64 `json:"dx,omitempty"`
	DY float64 `json:"dy,omitempty"`
	S  string  `json:"s,omitempty"`
}

type session struct {
	m        *Manager
	id       string
	holder   string
	clientID string
	remote   string
	started  time.Time
	params   Params
	// vsize and scale are what a virtual monitor is given.
	vsize protocol.Rect
	scale float64

	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticRTP

	ctx    context.Context
	cancel context.CancelFunc
	events chan message
	done   chan struct{}

	mu        sync.Mutex
	state     string
	lastInput time.Time
	status    Status
	client    map[string]any
	control   *webrtc.DataChannel
	videoOn   bool
	closeOnce sync.Once

	// Owned by the input worker.
	keys    map[string]input.Key
	buttons map[input.Button]bool
	wheelX  float64
	wheelY  float64

	// One continuous RTP stream per track across pipeline restarts; each is
	// owned by its source goroutine.
	videoSplice, audioSplice splicer
	audioTrack               *webrtc.TrackLocalStaticRTP
	audioOn                  atomic.Bool

	packets, frames, keyEvents, mouseEvents atomic.Uint64
}

func newSession(m *Manager, r OpenRequest, p Params) *session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{m: m, id: newID(), holder: r.Holder, clientID: r.ClientID, remote: r.RemoteAddr, started: m.o.Now(),
		params: p, ctx: ctx, cancel: cancel, events: make(chan message, eventBacklog), done: make(chan struct{}),
		state: "connecting", keys: map[string]input.Key{}, buttons: map[input.Button]bool{}}
	s.lastInput = s.started
	go s.inputWorker()
	return s
}

func (s *session) info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := Info{SessionID: s.id, Holder: s.holder, Remote: s.remote, State: s.state, StartedAt: s.started.UTC(),
		FPS: s.params.FPS, BitrateKbps: s.params.BitrateKbps, Capture: s.status.Capture, Encoder: s.status.Encoder,
		PacketsSent: s.packets.Load(), FramesSent: s.frames.Load(), KeyEvents: s.keyEvents.Load(), MouseEvents: s.mouseEvents.Load(),
		Client: s.client, Audio: s.audioOn.Load()}
	if s.keyEvents.Load()+s.mouseEvents.Load() > 0 {
		t := s.lastInput.UTC()
		i.LastInputAt = &t
	}
	return i
}

func (s *session) onConnectionState(state webrtc.PeerConnectionState) {
	switch state {
	case webrtc.PeerConnectionStateConnected:
		s.mu.Lock()
		s.state = "connected"
		start := !s.videoOn
		s.videoOn = true
		s.mu.Unlock()
		if start {
			// The encoder starts once the path is up, so its first keyframe
			// is not sent into a connection that is still being made.
			go s.video()
			if s.audioTrack != nil {
				go s.audio()
			}
		}
	case webrtc.PeerConnectionStateFailed:
		s.close("connection_lost")
	case webrtc.PeerConnectionStateClosed:
		s.close("closed")
	}
}

func (s *session) locked() bool { return s.m.o.Desktop != nil && s.m.o.Desktop().Locked }

func (s *session) video() {
	for s.ctx.Err() == nil {
		err := s.m.src.Run(s.ctx, s.params, s.emit, func(st Status) {
			s.mu.Lock()
			s.status = st
			s.mu.Unlock()
			s.send(map[string]any{"t": "status", "capture": st.Capture, "encoder": st.Encoder})
		})
		if err == nil || s.ctx.Err() != nil {
			return
		}
		// A locked desktop cannot be captured from this process. The session
		// is kept and the video resumes when the desktop is unlocked.
		if !s.locked() {
			logf("session %s: %v", s.id, err)
			s.send(map[string]any{"t": "error", "message": err.Error()})
			s.close("video_failed")
			return
		}
		for s.locked() {
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

// splicer joins the RTP streams of successive pipeline runs into one: a
// restarted pipeline begins a new stream with its own numbering.
type splicer struct {
	have    bool
	ssrc    uint32
	seqOff  uint16
	tsOff   uint32
	lastSeq uint16
	lastTS  uint32
}

// apply renumbers pkt; step is the timestamp gap left between two streams.
func (sp *splicer) apply(pkt *rtp.Packet, step uint32) {
	if !sp.have || pkt.SSRC != sp.ssrc {
		if sp.have {
			sp.seqOff = sp.lastSeq + 1 - pkt.SequenceNumber
			sp.tsOff = sp.lastTS + step - pkt.Timestamp
		}
		sp.have, sp.ssrc = true, pkt.SSRC
	}
	pkt.SequenceNumber += sp.seqOff
	pkt.Timestamp += sp.tsOff
	sp.lastSeq, sp.lastTS = pkt.SequenceNumber, pkt.Timestamp
}

// audio forwards the desktop's sound. It is optional: when it cannot be
// captured the session goes on without it.
func (s *session) audio() {
	err := s.m.aud.Run(s.ctx, func(pkt *rtp.Packet) {
		s.audioSplice.apply(pkt, 480)
		s.audioOn.Store(true)
		_ = s.audioTrack.WriteRTP(pkt)
	})
	s.audioOn.Store(false)
	if err != nil && s.ctx.Err() == nil {
		logf("session %s: audio: %v", s.id, err)
	}
}

// emit forwards one video packet; the viewer sees a single stream that
// resumes at the next keyframe after a pipeline restart.
func (s *session) emit(pkt *rtp.Packet) {
	s.videoSplice.apply(pkt, uint32(90000/s.params.FPS))
	s.packets.Add(1)
	if pkt.Marker {
		s.frames.Add(1)
	}
	_ = s.track.WriteRTP(pkt)
}

func (s *session) onDataChannel(dc *webrtc.DataChannel) {
	switch dc.Label() {
	case "input", "pointer":
		dc.OnMessage(func(msg webrtc.DataChannelMessage) { s.enqueue(msg.Data) })
	case "control":
		dc.OnOpen(func() {
			s.mu.Lock()
			s.control = dc
			s.mu.Unlock()
			s.send(map[string]any{"t": "hello", "session_id": s.id, "holder": s.holder})
			go s.cursor()
			go s.clipboard()
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) { s.onControl(msg.Data) })
	}
}

func (s *session) send(v any) {
	s.mu.Lock()
	dc := s.control
	s.mu.Unlock()
	if dc == nil {
		return
	}
	if data, err := json.Marshal(v); err == nil {
		_ = dc.SendText(string(data))
	}
}

func (s *session) onControl(data []byte) {
	if len(data) > maxMessage {
		return
	}
	var msg struct {
		T     string         `json:"t"`
		Stats map[string]any `json:"stats"`
	}
	if json.Unmarshal(data, &msg) != nil {
		return
	}
	switch msg.T {
	case "stats":
		s.mu.Lock()
		s.client = msg.Stats
		s.mu.Unlock()
	case "release":
		go s.close("released")
	}
}

func (s *session) enqueue(data []byte) {
	if len(data) > maxMessage {
		return
	}
	var msg message
	if json.Unmarshal(data, &msg) != nil {
		return
	}
	select {
	case s.events <- msg:
	default:
		// A full backlog means input is not being applied; stale moves are
		// the only events that can be dropped without losing meaning.
		if msg.T != "mm" {
			select {
			case s.events <- msg:
			case <-s.ctx.Done():
			}
		}
	}
}

// inputWorker applies events in order. Consecutive moves collapse into the
// latest one, so a slow desktop never falls behind the pointer.
func (s *session) inputWorker() {
	var pending *message
	for {
		var msg message
		if pending != nil {
			msg, pending = *pending, nil
		} else {
			select {
			case <-s.ctx.Done():
				return
			case msg = <-s.events:
			}
		}
		for msg.T == "mm" {
			select {
			case next := <-s.events:
				if next.T == "mm" {
					msg = next
					continue
				}
				pending = &next
			default:
			}
			break
		}
		s.apply(msg)
	}
}

func button(b int) (input.Button, bool) {
	switch b {
	case 0:
		return input.ButtonLeft, true
	case 1:
		return input.ButtonMiddle, true
	case 2:
		return input.ButtonRight, true
	}
	return "", false
}

// point clamps a position to the viewed display.
func (s *session) point(x, y int) input.Point {
	d := s.params.Display
	return input.Point{X: min(max(x, d.X), d.X+d.W-1), Y: min(max(y, d.Y), d.Y+d.H-1)}
}

func (s *session) apply(msg message) {
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	keyboard := false
	err := s.m.o.Input.Run(ctx, func(ctx context.Context, x *input.Exec) error {
		switch msg.T {
		case "mm":
			if msg.X == nil || msg.Y == nil {
				return nil
			}
			return x.MoveTo(ctx, s.point(*msg.X, *msg.Y), 0)
		case "md", "mu":
			btn, ok := button(msg.B)
			if !ok {
				return nil
			}
			// The position travels with the button so a click lands where
			// it was made even if a move on the unreliable channel was lost.
			if msg.X != nil && msg.Y != nil {
				if err := x.MoveTo(ctx, s.point(*msg.X, *msg.Y), 0); err != nil {
					return err
				}
			}
			down := msg.T == "md"
			if down {
				s.buttons[btn] = true
			} else {
				delete(s.buttons, btn)
			}
			return x.ButtonState(btn, down)
		case "mw":
			return s.wheel(x, msg.DX, msg.DY)
		case "kd", "ku":
			keyboard = true
			k, ok := keyForCode(msg.C)
			if !ok {
				return nil
			}
			down := msg.T == "kd"
			if down {
				s.keys[msg.C] = k
			} else {
				delete(s.keys, msg.C)
			}
			return x.KeyState(k, down)
		case "tx":
			keyboard = true
			if len(msg.S) == 0 || len(msg.S) > maxText {
				return nil
			}
			return x.Type(ctx, msg.S, 0, false)
		case "rel":
			s.releaseHeld(x)
		}
		return nil
	})
	if err != nil {
		if s.ctx.Err() == nil {
			logf("session %s: input %s: %v", s.id, msg.T, err)
		}
		return
	}
	if keyboard {
		s.keyEvents.Add(1)
	} else {
		s.mouseEvents.Add(1)
	}
	s.mu.Lock()
	s.lastInput = s.m.o.Now()
	s.mu.Unlock()
	if s.m.o.OnInput != nil {
		s.m.o.OnInput()
	}
}

// wheel scrolls by the browser's pixel deltas (positive dy scrolls down,
// positive dx right), keeping the fraction that has not been sent yet.
func (s *session) wheel(x *input.Exec, dx, dy float64) error {
	s.wheelX += dx * wheelPerPixel
	s.wheelY -= dy * wheelPerPixel
	ix, iy := int(s.wheelX), int(s.wheelY)
	if ix == 0 && iy == 0 {
		return nil
	}
	handled, err := x.ScrollDelta(ix, iy)
	if handled {
		s.wheelX -= float64(ix)
		s.wheelY -= float64(iy)
		return err
	}
	// Backends that scroll by whole notches get them as they accumulate.
	nx, ny := ix/input.WheelDelta, iy/input.WheelDelta
	if nx == 0 && ny == 0 {
		return nil
	}
	s.wheelX -= float64(nx * input.WheelDelta)
	s.wheelY -= float64(ny * input.WheelDelta)
	return x.Scroll(nx, ny)
}

// releaseHeld lets go of every key and button the session holds, so a lost
// connection or a page that lost focus never leaves one stuck down.
func (s *session) releaseHeld(x *input.Exec) {
	for code, k := range s.keys {
		_ = x.KeyState(k, false)
		delete(s.keys, code)
	}
	for btn := range s.buttons {
		_ = x.ButtonState(btn, false)
		delete(s.buttons, btn)
	}
}

// cursor sends the pointer shape whenever it changes; the page shows it as
// its own cursor, so moving the mouse is not delayed by the video.
func (s *session) cursor() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	var last uint64
	hidden := false
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(s.ctx, time.Second)
		state, err := s.m.o.Screen.Cursor(ctx)
		var img *cursorShape
		if err == nil && state.Visible {
			img, err = s.shape(ctx)
		}
		cancel()
		if err != nil {
			continue
		}
		if !state.Visible {
			if !hidden {
				hidden, last = true, 0
				s.send(map[string]any{"t": "cursor", "hidden": true})
			}
			continue
		}
		if img.sum != last || hidden {
			hidden, last = false, img.sum
			s.send(map[string]any{"t": "cursor", "w": img.w, "h": img.h, "hx": img.hx, "hy": img.hy, "png": img.png()})
		}
	}
}

type cursorShape struct {
	w, h, hx, hy int
	sum          uint64
	encode       func() []byte
}

func (c *cursorShape) png() string { return base64.StdEncoding.EncodeToString(c.encode()) }

func (s *session) shape(ctx context.Context) (*cursorShape, error) {
	ci, err := s.m.o.Screen.CursorImage(ctx)
	if err != nil {
		return nil, err
	}
	h := fnv.New64a()
	_, _ = h.Write(ci.Image.Pix)
	_, _ = h.Write([]byte(strconv.Itoa(ci.Hotspot.X) + "," + strconv.Itoa(ci.Hotspot.Y)))
	b := ci.Image.Bounds()
	return &cursorShape{w: b.Dx(), h: b.Dy(), hx: ci.Hotspot.X, hy: ci.Hotspot.Y, sum: h.Sum64() | 1, encode: func() []byte {
		var buf bytes.Buffer
		_ = png.Encode(&buf, ci.Image)
		return buf.Bytes()
	}}, nil
}

// clipboard tells the page when the desktop's clipboard changes, so text
// copied there can be pasted on the viewer's machine. Only changes made
// after the session began are sent.
func (s *session) clipboard() {
	cb := s.m.o.Clipboard
	if cb == nil {
		return
	}
	last, ok := cb.Sequence()
	if !ok {
		return
	}
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		seq, _ := cb.Sequence()
		if seq == last {
			continue
		}
		last = seq
		ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
		content, err := cb.Read(ctx)
		cancel()
		if err != nil || content.Text == nil || *content.Text == "" {
			continue
		}
		if len(*content.Text) > MaxClipboardText {
			s.send(map[string]any{"t": "clip", "large": true})
			continue
		}
		s.send(map[string]any{"t": "clip", "text": *content.Text})
	}
}

// watch ends a session that never connects or that has been idle too long,
// and tells the page when the desktop is locked or unlocked.
func (s *session) watch() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	wasLocked := false
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		if locked := s.locked(); locked != wasLocked {
			wasLocked = locked
			s.send(map[string]any{"t": "desk", "locked": locked})
		}
		s.m.tendDisplay(s.vsize, s.scale)
		now := s.m.o.Now()
		s.mu.Lock()
		state, idle := s.state, now.Sub(s.lastInput)
		s.mu.Unlock()
		switch {
		case state == "connecting" && now.Sub(s.started) > connectTimeout:
			s.close("connect_timeout")
			return
		case s.m.o.IdleRelease > 0 && idle > s.m.o.IdleRelease:
			s.close("idle")
			return
		}
	}
}

// close ends the session once: the page is told why, held keys and buttons
// are released, the encoder stops and the desktop becomes free.
func (s *session) close(reason string) {
	s.closeOnce.Do(func() {
		s.send(map[string]any{"t": "bye", "reason": reason})
		s.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), releaseKeysWithin)
		_ = s.m.o.Input.Run(ctx, func(_ context.Context, x *input.Exec) error {
			s.releaseHeld(x)
			return nil
		})
		cancel()
		if s.pc != nil {
			_ = s.pc.Close()
		}
		// The encoder has stopped by now; a replaced session leaves the
		// monitor for its successor.
		if reason != "replaced" {
			s.m.releaseDisplay()
		}
		s.m.release(s)
		s.m.event("remote_close", map[string]string{"session_id": s.id, "holder": s.holder, "remote": s.remote, "reason": reason,
			"duration_ms": strconv.FormatInt(s.m.o.Now().Sub(s.started).Milliseconds(), 10),
			"key_events":  strconv.FormatUint(s.keyEvents.Load(), 10), "mouse_events": strconv.FormatUint(s.mouseEvents.Load(), 10)})
		close(s.done)
	})
}
