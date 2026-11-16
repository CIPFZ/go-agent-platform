package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/CIPFZ/go-agent-platform/internal/actions/remote"
	"github.com/CIPFZ/go-agent-platform/internal/actions/session"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// idleSource is a remote desktop pipeline that produces no video.
type idleSource struct{}

func (idleSource) Available() error { return nil }

func (idleSource) Run(ctx context.Context, _ remote.Params, _ func(*rtp.Packet), _ func(remote.Status)) error {
	<-ctx.Done()
	return nil
}

func withRemote(d *Deps) {
	d.Config.Remote = true
	d.Config.RemoteUDPPort = 0
	d.RemoteSource = idleSource{}
	d.RemoteLoopback = true
}

// browserOffer builds what a page sends: an offer to receive video with the
// three data channels and its candidates gathered.
func browserOffer(t *testing.T) string {
	t.Helper()
	var se webrtc.SettingEngine
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetIncludeLoopbackCandidate(true)
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"input", "pointer", "control"} {
		if _, err := pc.CreateDataChannel(label, nil); err != nil {
			t.Fatal(err)
		}
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-done
	return pc.LocalDescription().SDP
}

func TestRemoteIsOffUnlessEnabled(t *testing.T) {
	e := newEnv(t)
	for _, r := range []reply{e.call("GET", "/remote/", nil), e.call("POST", "/v1/remote/sessions", map[string]any{"sdp": "x", "holder": "a"}),
		e.call("DELETE", "/v1/remote/sessions/rd-1", nil)} {
		if r.Status != 501 || r.Error == nil || r.Error.Code != protocol.CodeUnsupported {
			t.Fatalf("status %d error %+v", r.Status, r.Error)
		}
	}
	if r := e.call("GET", "/v1/remote/sessions", nil); r.Status != 200 || string(r.Result) != `{"sessions":[]}` {
		t.Fatalf("list = %d %s", r.Status, r.Result)
	}
	if r := e.call("GET", "/v1/capabilities", nil); strings.Contains(string(r.Result), `"remote"`) {
		t.Fatalf("capabilities list remote: %s", r.Result)
	}
}

func TestRemoteSessionHoldsTheDesktop(t *testing.T) {
	e := newEnv(t, withRemote)
	if r := e.call("GET", "/v1/capabilities", nil); !strings.Contains(string(r.Result), `"remote"`) {
		t.Fatalf("capabilities lack remote: %s", r.Result)
	}
	if r := e.call("GET", "/remote/", nil); r.Status != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") || !strings.Contains(string(r.Body), "<html") {
		t.Fatalf("page = %d %q", r.Status, r.Header.Get("Content-Type"))
	}
	click := map[string]any{"x": 1, "y": 1}
	if r := e.call("POST", "/v1/input/mouse/click", click); r.Status != 200 {
		t.Fatalf("click before the session = %d", r.Status)
	}

	r := e.call("POST", "/v1/remote/sessions", map[string]any{"sdp": browserOffer(t), "holder": "alice", "client_id": "tab-1", "fps": 60})
	var opened remote.OpenResult
	if r.Status != 200 || json.Unmarshal(r.Result, &opened) != nil || !strings.Contains(opened.SDP, "H264") || opened.FPS != 60 || opened.Display.W == 0 {
		t.Fatalf("open = %d %s %+v", r.Status, r.Result, r.Error)
	}

	// While alice holds the desktop the input API and other viewers are refused.
	for _, r := range []reply{e.call("POST", "/v1/input/mouse/click", click),
		e.call("POST", "/v1/input/batch", map[string]any{"actions": []any{map[string]any{"type": "wait", "ms": 1}}}),
		e.call("POST", "/v1/remote/sessions", map[string]any{"sdp": browserOffer(t), "holder": "bob", "client_id": "tab-2"})} {
		if r.Status != 409 || r.Error == nil || r.Error.Details["reason"] != protocol.ReasonRemoteInUse || r.Error.Details["holder"] != "alice" {
			t.Fatalf("status %d error %+v", r.Status, r.Error)
		}
	}
	// Observation is not affected.
	if r := e.call("GET", "/v1/screen/info", nil); r.Status != 200 {
		t.Fatalf("screen/info = %d", r.Status)
	}
	if r := e.call("GET", "/v1/remote/sessions", nil); !strings.Contains(string(r.Result), `"holder":"alice"`) || !strings.Contains(string(r.Result), opened.SessionID) {
		t.Fatalf("list = %s", r.Result)
	}

	if r := e.call("DELETE", "/v1/remote/sessions/"+opened.SessionID, nil); r.Status != 200 {
		t.Fatalf("delete = %d %+v", r.Status, r.Error)
	}
	if r := e.call("DELETE", "/v1/remote/sessions/"+opened.SessionID, nil); r.Status != 404 {
		t.Fatalf("second delete = %d", r.Status)
	}
	if r := e.call("POST", "/v1/input/mouse/click", click); r.Status != 200 {
		t.Fatalf("click after release = %d %+v", r.Status, r.Error)
	}

	kinds := map[string]string{}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && len(kinds) < 2; time.Sleep(10 * time.Millisecond) {
		for _, ev := range e.audit.List() {
			if strings.HasPrefix(ev.Kind, "remote_") {
				kinds[ev.Kind] = ev.Details["holder"] + "/" + ev.Details["reason"]
			}
		}
	}
	if kinds["remote_open"] != "alice/" || kinds["remote_close"] != "alice/released" {
		t.Fatalf("audit = %v", kinds)
	}
}

func TestRemoteNeedsTheDisplay(t *testing.T) {
	e := newEnv(t, withRemote)
	e.setSession(session.State{DisplayReady: true, Locked: true, Type: "console"})
	if r := e.call("POST", "/v1/remote/sessions", map[string]any{"sdp": browserOffer(t), "holder": "alice"}); r.Status != 503 || r.Error.Code != protocol.CodeNoDisplay {
		t.Fatalf("open while locked = %d %+v", r.Status, r.Error)
	}
	if r := e.call("GET", "/v1/capabilities", nil); strings.Contains(string(r.Result), `"remote"`) {
		t.Fatalf("capabilities list remote while locked: %s", r.Result)
	}
}
