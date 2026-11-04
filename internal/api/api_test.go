package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/clipboard"
	"github.com/CIPFZ/go-agent-platform/internal/actions/screen"
	"github.com/CIPFZ/go-agent-platform/internal/actions/session"
	"github.com/CIPFZ/go-agent-platform/internal/audit"
	"github.com/CIPFZ/go-agent-platform/internal/config"
	"github.com/CIPFZ/go-agent-platform/internal/dedup"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
)

type fakeScreen struct{}

func (fakeScreen) Displays() ([]screen.Display, error) {
	return []screen.Display{{Index: 0, Bounds: protocol.Rect{W: 800, H: 600}, Scale: 1.5, Primary: true}}, nil
}

func (fakeScreen) Capture(r protocol.Rect) (*image.RGBA, error) {
	return image.NewRGBA(image.Rect(0, 0, r.W, r.H)), nil
}

func (fakeScreen) Cursor() (screen.Cursor, error) {
	return screen.Cursor{X: 5, Y: 6, Visible: true}, nil
}

func (fakeScreen) CursorImage() (*screen.CursorImage, error) { return screen.DefaultArrow(), nil }

type testEnv struct {
	t     *testing.T
	api   *API
	http  *httptest.Server
	audit *audit.MemorySink
	mu    sync.Mutex
	state session.State
}

func (e *testEnv) setSession(st session.State) {
	e.mu.Lock()
	e.state = st
	e.mu.Unlock()
}

func newEnv(t *testing.T, mutate ...func(*Deps)) *testEnv {
	t.Helper()
	cfg, err := config.Parse([]string{"-job-dir", t.TempDir() + "/jobs", "-record-dir", t.TempDir() + "/records"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{t: t, audit: audit.NewMemorySink(1000), state: session.State{DisplayReady: true, Type: "console"}}
	d := Deps{
		Config:       cfg,
		Audit:        e.audit,
		Screen:       fakeScreen{},
		Input:        &fakeInput{},
		Window:       &fakeWindows{},
		Clipboard:    &fakeClipboard{},
		Session:      func() session.State { e.mu.Lock(); defer e.mu.Unlock(); return e.state },
		DPIAwareness: "per_monitor_v2",
	}
	for _, m := range mutate {
		m(&d)
	}
	a, err := NewWithDeps(d)
	if err != nil {
		t.Fatal(err)
	}
	e.api = a
	srv := server.New(server.Options{Audit: e.audit, Dedup: dedup.New(time.Minute, 0, nil), MaxConcurrency: 32}, a.Routes())
	a.SetStats(srv.Stats())
	e.http = httptest.NewServer(srv.Handler())
	t.Cleanup(func() { e.http.Close(); a.Shutdown() })
	return e
}

type reply struct {
	Status int
	Header http.Header
	Body   []byte
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *protocol.Error `json:"error"`
}

func (e *testEnv) call(method, path string, body any, headers ...string) reply {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		data, _ := json.Marshal(b)
		rd = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, e.http.URL+path, rd)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	r := reply{Status: resp.StatusCode, Header: resp.Header, Body: data}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(data, &r); err != nil {
			e.t.Fatalf("decode %s: %v", data, err)
		}
	}
	return r
}

func (r reply) into(t *testing.T, v any) {
	t.Helper()
	if !r.OK {
		t.Fatalf("request failed: status %d error %+v", r.Status, r.Error)
	}
	if err := json.Unmarshal(r.Result, v); err != nil {
		t.Fatalf("decode result %s: %v", r.Result, err)
	}
}

func (r reply) code(t *testing.T, want protocol.Code) {
	t.Helper()
	if r.OK || r.Error == nil || r.Error.Code != want {
		t.Fatalf("status %d body %s, want error %s", r.Status, r.Body, want)
	}
}

func TestProbe(t *testing.T) {
	e := newEnv(t)
	var p protocol.ProbeResult
	e.call("GET", "/v1/probe", nil).into(t, &p)
	if p.APIVersion != "v1" || p.Version == "" || p.LastInputAt != nil || !p.Display.Ready || p.Display.Count != 1 ||
		p.Display.Primary == nil || p.Display.Primary.W != 800 || p.Display.Primary.Scale != 1.5 ||
		p.Display.DPIAwareness != "per_monitor_v2" || p.SessionType != "console" || p.StartedAt.IsZero() {
		t.Fatalf("probe = %+v", p)
	}
	// last_input_at must be present as null before any input.
	if !bytes.Contains(e.call("GET", "/v1/probe", nil).Body, []byte(`"last_input_at":null`)) {
		t.Fatal("last_input_at not reported as null")
	}
}

func TestLockedSessionReportsAndRefusesDisplayActions(t *testing.T) {
	e := newEnv(t)
	e.setSession(session.State{DisplayReady: true, Locked: true, Type: "console"})
	var p protocol.ProbeResult
	e.call("GET", "/v1/probe", nil).into(t, &p)
	if !p.SessionLocked || p.Display.Ready {
		t.Fatalf("probe while locked = %+v", p)
	}
	var caps protocol.CapabilitiesResult
	e.call("GET", "/v1/capabilities", nil).into(t, &caps)
	for _, a := range caps.Actions {
		switch a {
		case "screen", "input", "window", "clipboard", "record":
			t.Fatalf("display action %q advertised while locked", a)
		}
	}
	r := e.call("POST", "/v1/screen/capture", `{}`)
	r.code(t, protocol.CodeNoDisplay)
	if r.Status != 503 || !r.Error.Retryable {
		t.Fatalf("no_display must be 503 and retryable: %d %+v", r.Status, r.Error)
	}
}

func TestCapabilitiesLimits(t *testing.T) {
	e := newEnv(t)
	var caps protocol.CapabilitiesResult
	e.call("GET", "/v1/capabilities", nil).into(t, &caps)
	want := protocol.Limits{
		JSONBodyMaxBytes: 8388608, MultipartMaxBytes: 1073741824, ExecOutputMaxBytes: 67108864,
		FileReadMaxBytes: 67108864, DownloadMaxBytes: 1073741824, BatchMaxActions: 200,
		MaxConcurrency: 32, SyncTimeoutMaxMS: 600000, TransferTimeoutMaxMS: 3600000,
	}
	if caps.Limits != want || caps.Platform == "" || caps.Session.Type != "console" {
		t.Fatalf("capabilities = %+v", caps)
	}
}

func TestScreenCaptureBase64AndStream(t *testing.T) {
	e := newEnv(t)
	var shot protocol.CaptureResult
	e.call("POST", "/v1/screen/capture", map[string]any{"format": "png", "region": map[string]int{"x": 10, "y": 10, "w": 30, "h": 20}}).into(t, &shot)
	data, err := base64.StdEncoding.DecodeString(shot.Image)
	if err != nil || shot.Width != 30 || shot.Height != 20 || shot.Format != "png" || len(data) == 0 || shot.Cursor != (protocol.Point{X: 5, Y: 6}) {
		t.Fatalf("shot = %+v err=%v", shot, err)
	}
	r := e.call("POST", "/v1/screen/capture", map[string]any{"encoding": "stream"})
	if r.Status != 200 || r.Header.Get("Content-Type") != "image/jpeg" || r.Header.Get(protocol.HeaderResultWidth) != "800" ||
		r.Header.Get(protocol.HeaderResultSize) != itoa(len(r.Body)) || r.Header.Get(protocol.HeaderCursorX) != "5" || r.Header.Get(protocol.HeaderCapturedAt) == "" {
		t.Fatalf("stream status=%d headers=%v", r.Status, r.Header)
	}
	e.call("POST", "/v1/screen/capture", `{"encoding":"hex"}`).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/screen/capture", `{"display":3}`).code(t, protocol.CodeInvalidRequest)
}

func TestScreenInfoAndCursor(t *testing.T) {
	e := newEnv(t)
	var info struct {
		Displays []screen.Display `json:"displays"`
	}
	e.call("GET", "/v1/screen/info", nil).into(t, &info)
	if len(info.Displays) != 1 || !info.Displays[0].Primary {
		t.Fatalf("info = %+v", info)
	}
	var c screen.Cursor
	e.call("GET", "/v1/screen/cursor", nil).into(t, &c)
	if c != (screen.Cursor{X: 5, Y: 6, Visible: true}) {
		t.Fatalf("cursor = %+v", c)
	}
}

func TestCaptureAuditsSizeAndHash(t *testing.T) {
	e := newEnv(t)
	e.call("POST", "/v1/screen/capture", `{}`)
	last := e.lastAudit("screen.capture")
	if last.Action != "screen.capture" || last.Params["width"] != "800" || len(last.Params["sha256"]) != 64 {
		t.Fatalf("audit = %+v", last)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// auditEvents waits until at least n audit events exist. The server records
// an event only after the response has been flushed, so a test can see the
// response before its audit record.
func (e *testEnv) auditEvents(n int) []audit.Event {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ev := e.audit.List()
		if len(ev) >= n || time.Now().After(deadline) {
			if len(ev) < n {
				e.t.Fatalf("only %d of %d audit events recorded", len(ev), n)
			}
			return ev
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// lastAudit waits for the next audit event of action after the call that
// was just made and returns the most recent one.
func (e *testEnv) lastAudit(action string) audit.Event {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ev := e.audit.List()
		for i := len(ev) - 1; i >= 0; i-- {
			if ev[i].Action == action {
				return ev[i]
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("no audit event for %s", action)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type fakeClipboard struct {
	mu      sync.Mutex
	content clipboard.Content
}

func (f *fakeClipboard) Read(context.Context) (clipboard.Content, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.content, nil
}

func (f *fakeClipboard) WriteText(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.content = clipboard.Content{Formats: []string{"UTF8_STRING"}, Text: &text}
	return nil
}

func (f *fakeClipboard) WritePNG(_ context.Context, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.content = clipboard.Content{Formats: []string{"image/png"}, PNG: data}
	return nil
}
