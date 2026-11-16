// Package remote implements the web remote desktop
// (docs/remote-desktop-design.md): a browser receives the desktop as a WebRTC
// video track and sends mouse and keyboard events back over data channels.
// One person uses the desktop at a time; the session is the lease.
package remote

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"

	"github.com/CIPFZ/go-agent-platform/internal/actions/clipboard"
	"github.com/CIPFZ/go-agent-platform/internal/actions/input"
	"github.com/CIPFZ/go-agent-platform/internal/actions/screen"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Page is the viewer served at /remote/.
//
//go:embed web/index.html
var Page []byte

// Limits of a session request.
const (
	DefaultFPS     = 30
	MinFPS         = 5
	MaxFPS         = 60
	MinBitrateKbps = 500
	MaxBitrateKbps = 50000
	// MaxHolder bounds the display name of the person using the desktop.
	MaxHolder = 64
)

// Timing of a session's life cycle.
const (
	connectTimeout    = 20 * time.Second
	iceDisconnected   = 5 * time.Second
	iceFailed         = 30 * time.Second
	iceKeepAlive      = 2 * time.Second
	releaseKeysWithin = 2 * time.Second
)

// Options configures a Manager.
type Options struct {
	// FFmpeg is the resolved ffmpeg executable; empty disables the service.
	FFmpeg string
	// UDPPort carries the WebRTC media and data of every session; 0 picks a
	// free port (tests).
	UDPPort int
	// IdleRelease ends a session that has sent no input for this long; 0
	// never does.
	IdleRelease time.Duration
	Input       *input.Service
	Screen      *screen.Service
	// Clipboard lets the page follow the desktop's clipboard; nil turns
	// that off.
	Clipboard *clipboard.Service
	// Desktop reports whether the desktop is locked or showing an elevation
	// prompt, which this process can neither capture nor operate; nil means
	// it never is.
	Desktop func() Desktop
	// Source overrides the ffmpeg pipeline (tests).
	Source Source
	// Audio overrides the capture of the desktop's sound (tests).
	Audio AudioSource
	// Loopback also offers loopback addresses, for a viewer on this machine.
	Loopback bool
	Now      func() time.Time
	// OnInput is called after every input event that was applied.
	OnInput func()
	// OnEvent records session life cycle events for the audit log.
	OnEvent func(kind string, details map[string]string)
}

// Desktop is the state of the interactive desktop.
type Desktop struct {
	Locked bool
}

// MaxClipboardText is the longest clipboard text sent to the page inside a
// control message; longer text is fetched by the page from /v1/clipboard.
const MaxClipboardText = 32 << 10

// Manager owns the single remote session.
type Manager struct {
	o   Options
	src Source
	aud AudioSource

	initOnce sync.Once
	initErr  error
	api      *webrtc.API
	mux      *ice.MultiUDPMuxDefault

	mu     sync.Mutex
	cur    *session
	closed bool
}

// New returns a Manager. Nothing is opened until the first session.
func New(o Options) *Manager {
	if o.Now == nil {
		o.Now = time.Now
	}
	src := o.Source
	if src == nil {
		src = newFFmpegSource(o.FFmpeg)
	}
	aud := o.Audio
	if aud == nil {
		aud = newFFmpegAudio(o.FFmpeg)
	}
	return &Manager{o: o, src: src, aud: aud}
}

// Available reports why sessions cannot be opened, or nil.
func (m *Manager) Available() error { return m.src.Available() }

func (m *Manager) init() error {
	m.initOnce.Do(func() {
		opts := []ice.UDPMuxFromPortOption{ice.UDPMuxFromPortWithNetworks(ice.NetworkTypeUDP4)}
		if m.o.Loopback {
			opts = append(opts, ice.UDPMuxFromPortWithLoopback())
		}
		mux, err := ice.NewMultiUDPMuxFromPort(m.o.UDPPort, opts...)
		if err != nil {
			m.initErr = protocol.Errorf(protocol.CodeIOFailed, "listen on UDP port %d for the remote desktop: %v", m.o.UDPPort, err)
			return
		}
		media := &webrtc.MediaEngine{}
		feedback := []webrtc.RTCPFeedback{{Type: "goog-remb"}, {Type: "ccm", Parameter: "fir"}, {Type: "nack"}, {Type: "nack", Parameter: "pli"}}
		// Browsers offer several H.264 profiles; whichever is agreed, the
		// stream is what the encoder produces and their decoders accept it.
		for i, profile := range []string{"42e01f", "42001f", "4d001f", "64001f", "640032"} {
			if err := media.RegisterCodec(webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
					SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=" + profile,
					RTCPFeedback: feedback},
				PayloadType: webrtc.PayloadType(102 + i),
			}, webrtc.RTPCodecTypeVideo); err != nil {
				m.initErr = err
				return
			}
		}
		if err := media.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
				SDPFmtpLine: "minptime=10;useinbandfec=1"},
			PayloadType: 111,
		}, webrtc.RTPCodecTypeAudio); err != nil {
			m.initErr = err
			return
		}
		registry := &interceptor.Registry{}
		if err := webrtc.RegisterDefaultInterceptors(media, registry); err != nil {
			m.initErr = err
			return
		}
		var se webrtc.SettingEngine
		se.SetICEUDPMux(mux)
		se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
		// Browsers hide their addresses behind mDNS names; the connection is
		// made from their checks against this side's addresses instead.
		se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
		se.SetICETimeouts(iceDisconnected, iceFailed, iceKeepAlive)
		se.SetIncludeLoopbackCandidate(m.o.Loopback)
		m.mux = mux
		m.api = webrtc.NewAPI(webrtc.WithMediaEngine(media), webrtc.WithInterceptorRegistry(registry), webrtc.WithSettingEngine(se))
	})
	return m.initErr
}

// OpenRequest asks for the desktop.
type OpenRequest struct {
	SDP string
	// Holder names the person, shown to others while the desktop is taken.
	Holder string
	// ClientID identifies a browser tab so its reconnect replaces its own
	// earlier session instead of being refused.
	ClientID    string
	FPS         int
	BitrateKbps int
	Display     int
	// RemoteAddr is the caller's address, for the session list and audit.
	RemoteAddr string
}

// OpenResult answers an OpenRequest.
type OpenResult struct {
	SessionID     string        `json:"session_id"`
	SDP           string        `json:"sdp"`
	Display       protocol.Rect `json:"display"`
	Scale         float64       `json:"scale"`
	FPS           int           `json:"fps"`
	BitrateKbps   int           `json:"bitrate_kbps"`
	IdleReleaseMS int64         `json:"idle_release_ms"`
}

// Info describes the session for the list endpoint.
type Info struct {
	SessionID   string     `json:"session_id"`
	Holder      string     `json:"holder"`
	Remote      string     `json:"remote"`
	State       string     `json:"state"`
	StartedAt   time.Time  `json:"started_at"`
	LastInputAt *time.Time `json:"last_input_at"`
	FPS         int        `json:"fps"`
	BitrateKbps int        `json:"bitrate_kbps"`
	Capture     string     `json:"capture"`
	Encoder     string     `json:"encoder"`
	// Audio is true while the desktop's sound is being sent.
	Audio       bool   `json:"audio"`
	PacketsSent uint64 `json:"packets_sent"`
	FramesSent  uint64 `json:"frames_sent"`
	KeyEvents   uint64 `json:"key_events"`
	MouseEvents uint64 `json:"mouse_events"`
	// Client is what the viewer last reported about its decoder.
	Client map[string]any `json:"client,omitempty"`
}

// InUse returns the error given to anyone else while the desktop is taken,
// or nil when it is free.
func (m *Manager) InUse() error {
	m.mu.Lock()
	s := m.cur
	m.mu.Unlock()
	if s == nil {
		return nil
	}
	return inUse(s)
}

func inUse(s *session) *protocol.Error {
	return protocol.Errorf(protocol.CodeConflict, "the desktop is in use by %s since %s", s.holder, s.started.UTC().Format(time.RFC3339)).
		WithReason(protocol.ReasonRemoteInUse).WithDetail("holder", s.holder).WithDetail("since", s.started.UTC().Format(time.RFC3339))
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "rd-" + hex.EncodeToString(b[:])
}

// defaultBitrate scales with the picture: 8 Mbit/s at 2560x1600.
func defaultBitrate(r protocol.Rect) int {
	return min(max(r.W*r.H/512, 2000), 20000)
}

// Open starts a session from a browser's offer and returns the answer.
func (m *Manager) Open(ctx context.Context, r OpenRequest) (*OpenResult, error) {
	if err := m.Available(); err != nil {
		return nil, err
	}
	r.Holder = strings.TrimSpace(r.Holder)
	switch {
	case strings.TrimSpace(r.SDP) == "":
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "sdp is required")
	case r.Holder == "" || len(r.Holder) > MaxHolder:
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "holder must be 1 to %d bytes", MaxHolder)
	case r.FPS != 0 && (r.FPS < MinFPS || r.FPS > MaxFPS):
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "fps must be %d to %d", MinFPS, MaxFPS)
	case r.BitrateKbps != 0 && (r.BitrateKbps < MinBitrateKbps || r.BitrateKbps > MaxBitrateKbps):
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "bitrate_kbps must be %d to %d", MinBitrateKbps, MaxBitrateKbps)
	case r.Display != 0:
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "only the primary display (0) can be viewed")
	}
	displays, err := m.o.Screen.Displays(ctx)
	if err != nil {
		return nil, err
	}
	if len(displays) == 0 {
		return nil, protocol.Errorf(protocol.CodeNoDisplay, "no display")
	}
	display := displays[0]
	p := Params{FPS: r.FPS, BitrateKbps: r.BitrateKbps, Display: display.Bounds}
	if p.FPS == 0 {
		p.FPS = DefaultFPS
	}
	if p.BitrateKbps == 0 {
		p.BitrateKbps = defaultBitrate(display.Bounds)
	}

	// The lease is taken before any slow work so two callers cannot both
	// proceed; a failed setup releases it.
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, protocol.Errorf(protocol.CodeIOFailed, "vm-agent is shutting down")
	}
	var replaced *session
	if cur := m.cur; cur != nil {
		if r.ClientID == "" || cur.clientID != r.ClientID {
			m.mu.Unlock()
			return nil, inUse(cur)
		}
		replaced = cur
	}
	s := newSession(m, r, p)
	m.cur = s
	m.mu.Unlock()
	if replaced != nil {
		replaced.close("replaced")
	}

	answer, err := m.connect(ctx, s, r.SDP)
	if err != nil {
		s.close("setup_failed")
		return nil, err
	}
	m.event("remote_open", map[string]string{"session_id": s.id, "holder": s.holder, "remote": s.remote})
	go s.watch()
	return &OpenResult{SessionID: s.id, SDP: answer, Display: display.Bounds, Scale: display.Scale, FPS: p.FPS,
		BitrateKbps: p.BitrateKbps, IdleReleaseMS: m.o.IdleRelease.Milliseconds()}, nil
}

func (m *Manager) connect(ctx context.Context, s *session, offer string) (string, error) {
	if err := m.init(); err != nil {
		return "", err
	}
	pc, err := m.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", protocol.Errorf(protocol.CodeIOFailed, "create the peer connection: %v", err)
	}
	s.pc = pc
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "video", "vm-agent")
	if err != nil {
		return "", err
	}
	s.track = track
	sender, err := pc.AddTrack(track)
	if err != nil {
		return "", protocol.Errorf(protocol.CodeIOFailed, "add the video track: %v", err)
	}
	// Reading RTCP drives the interceptors (retransmissions, reports).
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()
	// Sound is sent when the page asks for it and it can be captured.
	if strings.Contains(offer, "m=audio") && m.aud.Available() == nil {
		audio, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "vm-agent-audio")
		if err != nil {
			return "", err
		}
		audioSender, err := pc.AddTrack(audio)
		if err != nil {
			return "", protocol.Errorf(protocol.CodeIOFailed, "add the audio track: %v", err)
		}
		go func() {
			buf := make([]byte, 1500)
			for {
				if _, _, err := audioSender.Read(buf); err != nil {
					return
				}
			}
		}()
		s.audioTrack = audio
	}
	pc.OnDataChannel(s.onDataChannel)
	pc.OnConnectionStateChange(s.onConnectionState)
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return "", protocol.Errorf(protocol.CodeInvalidRequest, "sdp is not a usable offer: %v", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", protocol.Errorf(protocol.CodeInvalidRequest, "sdp cannot be answered (it must offer H.264 video): %v", err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return "", protocol.Errorf(protocol.CodeIOFailed, "set the local description: %v", err)
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return pc.LocalDescription().SDP, nil
}

// Sessions lists the current session, if any.
func (m *Manager) Sessions() []Info {
	m.mu.Lock()
	s := m.cur
	m.mu.Unlock()
	if s == nil {
		return []Info{}
	}
	return []Info{s.info()}
}

// Close ends the session with the given id, releasing the desktop.
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	s := m.cur
	m.mu.Unlock()
	if s == nil || s.id != id {
		return protocol.Errorf(protocol.CodeNotFound, "no remote session %q", id)
	}
	s.close("released")
	return nil
}

// Shutdown ends the session and refuses new ones.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	s := m.cur
	m.mu.Unlock()
	if s != nil {
		s.close("agent_shutdown")
	}
	if m.mux != nil {
		_ = m.mux.Close()
	}
}

// UDPAddrs returns the addresses the media socket is bound to (tests).
func (m *Manager) UDPAddrs() []net.Addr {
	if m.mux == nil {
		return nil
	}
	return m.mux.GetListenAddresses()
}

func (m *Manager) release(s *session) {
	m.mu.Lock()
	if m.cur == s {
		m.cur = nil
	}
	m.mu.Unlock()
}

func (m *Manager) event(kind string, details map[string]string) {
	if m.o.OnEvent != nil {
		m.o.OnEvent(kind, details)
	}
}

func logf(format string, args ...any) { log.Printf("vm-agent: remote: "+format, args...) }
