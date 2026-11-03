package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/async"
	"github.com/CIPFZ/go-agent-platform/internal/audit"
	"github.com/CIPFZ/go-agent-platform/internal/dedup"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/version"
)

const rid = "test-request-0001"

type envelope struct {
	OK        bool            `json:"ok"`
	RequestID string          `json:"request_id"`
	ElapsedMS *int64          `json:"elapsed_ms"`
	Result    json.RawMessage `json:"result"`
	Error     *protocol.Error `json:"error"`
}

type harness struct {
	srv   *Server
	http  *httptest.Server
	audit *audit.MemorySink
	calls atomic.Int64
	gate  chan struct{}
}

func newHarness(t *testing.T, maxConcurrency int, extra ...*Route) *harness {
	t.Helper()
	h := &harness{audit: audit.NewMemorySink(100), gate: make(chan struct{})}
	routes := []*Route{
		{Method: "POST", Path: "/v1/act", Action: "test.act", Class: NonRetryable, Handler: func(c *Call) (any, error) {
			n := h.calls.Add(1)
			var req struct {
				Fail  bool `json:"fail"`
				Block bool `json:"block"`
			}
			if err := c.Decode(&req); err != nil {
				return nil, err
			}
			c.Param("n", "x")
			if req.Block {
				<-h.gate
			}
			if req.Fail {
				return nil, protocol.Errorf(protocol.CodeNotFound, "nothing")
			}
			return map[string]int64{"n": n}, nil
		}},
		{Method: "POST", Path: "/v1/read", Action: "test.read", Class: Retryable, Handler: func(c *Call) (any, error) {
			return map[string]int64{"n": h.calls.Add(1)}, nil
		}},
		{Method: "POST", Path: "/v1/slow", Action: "test.slow", Class: Retryable, Handler: func(c *Call) (any, error) {
			return async.Run(c.Context(50*time.Millisecond), func() (any, error) {
				time.Sleep(time.Second)
				return nil, nil
			})
		}},
		{Method: "POST", Path: "/v1/slow-write", Action: "test.slow_write", Class: NonRetryable, Handler: func(c *Call) (any, error) {
			ctx := c.Context(time.Hour)
			<-ctx.Done()
			return nil, ctx.Err()
		}},
		{Method: "POST", Path: "/v1/stream", Action: "test.stream", Class: NonRetryable, Handler: func(c *Call) (any, error) {
			return &Stream{ContentType: "application/octet-stream", Size: 3, Header: map[string]string{protocol.HeaderResultSize: "3"}, Body: strings.NewReader("abc")}, nil
		}},
		{Method: "GET", Path: "/v1/panic", Action: "test.panic", Class: Retryable, Handler: func(c *Call) (any, error) {
			panic("boom")
		}},
	}
	routes = append(routes, extra...)
	h.srv = New(Options{Audit: h.audit, Dedup: dedup.New(time.Minute, 0, nil), MaxConcurrency: maxConcurrency}, routes)
	h.http = httptest.NewServer(h.srv.Handler())
	t.Cleanup(h.http.Close)
	return h
}

func (h *harness) do(t *testing.T, method, path, body string, headers map[string]string) (*http.Response, envelope) {
	t.Helper()
	resp, env, err := h.try(method, path, body, headers)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp, env
}

// try is do without testing.T so it can run on other goroutines.
func (h *harness) try(method, path, body string, headers map[string]string) (*http.Response, envelope, error) {
	var env envelope
	req, _ := http.NewRequest(method, h.http.URL+path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, env, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, env, err
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return resp, env, nil
}

func TestEnvelopeAndVersionHeader(t *testing.T) {
	h := newHarness(t, 4)
	resp, env := h.do(t, "POST", "/v1/act", `{}`, map[string]string{protocol.HeaderRequestID: rid})
	if resp.StatusCode != 200 || !env.OK || env.RequestID != rid || env.ElapsedMS == nil || string(env.Result) != `{"n":1}` {
		t.Fatalf("status=%d env=%+v result=%s", resp.StatusCode, env, env.Result)
	}
	if resp.Header.Get(protocol.HeaderAgentVersion) != version.Version {
		t.Fatalf("missing X-Agent-Version")
	}
	resp, env = h.do(t, "POST", "/v1/read", ``, nil)
	if !env.OK || len(env.RequestID) != 32 {
		t.Fatalf("generated request id = %q", env.RequestID)
	}
}

func TestErrorEnvelope(t *testing.T) {
	h := newHarness(t, 4)
	resp, env := h.do(t, "POST", "/v1/act", `{"fail":true}`, nil)
	if resp.StatusCode != 404 || env.OK || env.Error == nil || env.Error.Code != protocol.CodeNotFound || env.ElapsedMS != nil || env.Result != nil {
		t.Fatalf("status=%d env=%+v", resp.StatusCode, env)
	}
}

func TestRequestIDFormat(t *testing.T) {
	h := newHarness(t, 4)
	for _, id := range []string{"short", strings.Repeat("a", 129), "has space in it 0123", "slash/is/not/allowed"} {
		resp, env := h.do(t, "POST", "/v1/act", `{}`, map[string]string{protocol.HeaderRequestID: id})
		if resp.StatusCode != 400 || env.Error.Code != protocol.CodeInvalidRequest || env.Error.Details["reason"] != protocol.ReasonInvalidRequestID {
			t.Errorf("id %q: status=%d env=%+v", id, resp.StatusCode, env.Error)
		}
	}
	if h.calls.Load() != 0 {
		t.Fatal("handler ran for an invalid request id")
	}
	for _, id := range []string{strings.Repeat("a", 16), strings.Repeat("Z", 128), "abc.DEF_123-456.x"} {
		if _, env := h.do(t, "POST", "/v1/read", `{}`, map[string]string{protocol.HeaderRequestID: id}); !env.OK {
			t.Errorf("id %q rejected: %+v", id, env.Error)
		}
	}
}

func TestDedupReplaysCompletedResponse(t *testing.T) {
	h := newHarness(t, 4)
	hdr := map[string]string{protocol.HeaderRequestID: rid}
	_, first := h.do(t, "POST", "/v1/act", `{}`, hdr)
	resp, second := h.do(t, "POST", "/v1/act", `{}`, hdr)
	if h.calls.Load() != 1 {
		t.Fatalf("handler ran %d times", h.calls.Load())
	}
	if resp.StatusCode != 200 || resp.Header.Get(protocol.HeaderDedupHit) != "true" || string(second.Result) != string(first.Result) || *second.ElapsedMS != *first.ElapsedMS {
		t.Fatalf("replay status=%d hit=%q first=%+v second=%+v", resp.StatusCode, resp.Header.Get(protocol.HeaderDedupHit), first, second)
	}
	events := h.audit.List()
	if last := events[len(events)-1]; !last.DedupHit || last.Code != "ok" {
		t.Fatalf("audit of replay = %+v", last)
	}
}

func TestDedupReplaysErrorsWithStatus200(t *testing.T) {
	h := newHarness(t, 4)
	hdr := map[string]string{protocol.HeaderRequestID: rid}
	first, _ := h.do(t, "POST", "/v1/act", `{"fail":true}`, hdr)
	resp, env := h.do(t, "POST", "/v1/act", `{"fail":true}`, hdr)
	if first.StatusCode != 404 || resp.StatusCode != 200 || env.OK || env.Error.Code != protocol.CodeNotFound || h.calls.Load() != 1 {
		t.Fatalf("first=%d replay=%d env=%+v calls=%d", first.StatusCode, resp.StatusCode, env, h.calls.Load())
	}
}

func TestDedupInFlightConflict(t *testing.T) {
	h := newHarness(t, 4)
	hdr := map[string]string{protocol.HeaderRequestID: rid}
	done := make(chan envelope)
	go func() {
		_, env, _ := h.try("POST", "/v1/act", `{"block":true}`, hdr)
		done <- env
	}()
	for h.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	resp, env := h.do(t, "POST", "/v1/act", `{"block":true}`, hdr)
	if resp.StatusCode != 409 || env.Error.Code != protocol.CodeConflict || env.Error.Details["reason"] != protocol.ReasonInFlight {
		t.Fatalf("status=%d env=%+v", resp.StatusCode, env.Error)
	}
	close(h.gate)
	if first := <-done; !first.OK {
		t.Fatalf("first request = %+v", first)
	}
	if h.calls.Load() != 1 {
		t.Fatalf("handler ran %d times", h.calls.Load())
	}
}

func TestDedupKeyIsolationAndRetryableRoutes(t *testing.T) {
	var h *harness
	h = newHarness(t, 4, &Route{Method: "POST", Path: "/v1/act2", Action: "test.act2", Class: NonRetryable, Handler: func(c *Call) (any, error) {
		return map[string]int64{"n": h.calls.Add(1)}, nil
	}})
	hdr := map[string]string{protocol.HeaderRequestID: rid}
	h.do(t, "POST", "/v1/act", `{}`, hdr)
	h.do(t, "POST", "/v1/act2", `{}`, hdr)
	h.do(t, "POST", "/v1/read", `{}`, hdr)
	h.do(t, "POST", "/v1/read", `{}`, hdr)
	h.do(t, "POST", "/v1/act", `{}`, nil)
	if h.calls.Load() != 5 {
		t.Fatalf("calls = %d, want 5 (different paths, retryable routes and missing ids never dedup)", h.calls.Load())
	}
}

func TestDedupStreamConflict(t *testing.T) {
	h := newHarness(t, 4)
	hdr := map[string]string{protocol.HeaderRequestID: rid}
	resp, _ := h.do(t, "POST", "/v1/stream", ``, hdr)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "abc" || resp.Header.Get("Content-Type") != "application/octet-stream" || resp.Header.Get(protocol.HeaderResultSize) != "3" {
		t.Fatalf("stream status=%d body=%q headers=%v", resp.StatusCode, body, resp.Header)
	}
	resp, env := h.do(t, "POST", "/v1/stream", ``, hdr)
	if resp.StatusCode != 409 || env.Error.Details["reason"] != protocol.ReasonDedupStream {
		t.Fatalf("repeat = %d %+v", resp.StatusCode, env.Error)
	}
}

func TestBusyWhenConcurrencyExhausted(t *testing.T) {
	h := newHarness(t, 1)
	go h.try("POST", "/v1/act", `{"block":true}`, nil)
	for h.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	resp, env := h.do(t, "POST", "/v1/read", `{}`, nil)
	if resp.StatusCode != 503 || env.Error.Code != protocol.CodeBusy || !env.Error.Retryable {
		t.Fatalf("status=%d env=%+v", resp.StatusCode, env.Error)
	}
	close(h.gate)
}

func TestTimeoutRetryableFollowsClass(t *testing.T) {
	h := newHarness(t, 4)
	start := time.Now()
	resp, env := h.do(t, "POST", "/v1/slow", `{}`, nil)
	if resp.StatusCode != 504 || env.Error.Code != protocol.CodeTimeout || !env.Error.Retryable || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("read timeout status=%d env=%+v after %s", resp.StatusCode, env.Error, time.Since(start))
	}
	resp, env = h.do(t, "POST", "/v1/slow-write", `{}`, map[string]string{protocol.HeaderTimeoutMS: "30"})
	if resp.StatusCode != 504 || env.Error.Retryable {
		t.Fatalf("write timeout status=%d env=%+v", resp.StatusCode, env.Error)
	}
	for _, bad := range []string{"0", "-5", "abc", "1.5"} {
		resp, env = h.do(t, "POST", "/v1/read", `{}`, map[string]string{protocol.HeaderTimeoutMS: bad})
		if resp.StatusCode != 400 || env.Error.Details["reason"] != protocol.ReasonInvalidTimeout {
			t.Errorf("X-Timeout-Ms %q: status=%d", bad, resp.StatusCode)
		}
	}
}

func TestTimeoutHeaderIsCapped(t *testing.T) {
	var got time.Duration
	h := newHarness(t, 4,
		&Route{Method: "POST", Path: "/v1/t", Action: "t", Handler: func(c *Call) (any, error) { got = c.Timeout(time.Second); return nil, nil }},
		&Route{Method: "POST", Path: "/v1/tx", Action: "tx", Transfer: true, Handler: func(c *Call) (any, error) { got = c.Timeout(time.Second); return nil, nil }})
	cases := []struct {
		path, header string
		want         time.Duration
	}{
		{"/v1/t", "", time.Second},
		{"/v1/t", "2500", 2500 * time.Millisecond},
		{"/v1/t", "99999999999", SyncTimeoutMax},
		{"/v1/tx", "99999999999", TransferTimeout},
		{"/v1/tx", "1200000", 20 * time.Minute},
	}
	for _, tc := range cases {
		hdr := map[string]string{}
		if tc.header != "" {
			hdr[protocol.HeaderTimeoutMS] = tc.header
		}
		h.do(t, "POST", tc.path, `{}`, hdr)
		if got != tc.want {
			t.Errorf("%s with %q: timeout %s, want %s", tc.path, tc.header, got, tc.want)
		}
	}
}

func TestBodyLimitsAndJSONErrors(t *testing.T) {
	h := newHarness(t, 4)
	big := `{"fail":false,"x":"` + strings.Repeat("a", int(JSONBodyMax)) + `"}`
	resp, env := h.do(t, "POST", "/v1/act", big, nil)
	if resp.StatusCode != 413 || env.Error.Code != protocol.CodeTooLarge {
		t.Fatalf("oversized body: %d %+v", resp.StatusCode, env.Error)
	}
	for _, body := range []string{`{`, `{"unknown":1}`, `[]`, `{} {}`} {
		resp, env = h.do(t, "POST", "/v1/act", body, nil)
		if resp.StatusCode != 400 || env.Error.Details["reason"] != protocol.ReasonInvalidJSON {
			t.Errorf("body %q: %d %+v", body, resp.StatusCode, env.Error)
		}
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	h := newHarness(t, 4)
	resp, env := h.do(t, "GET", "/v1/nope", ``, nil)
	if resp.StatusCode != 404 || env.Error.Details["reason"] != protocol.ReasonUnknownRoute {
		t.Fatalf("unknown route: %d %+v", resp.StatusCode, env.Error)
	}
	resp, env = h.do(t, "GET", "/v1/act", ``, nil)
	if resp.StatusCode != 400 || env.Error.Details["reason"] != protocol.ReasonMethodNotAllowed {
		t.Fatalf("wrong method: %d %+v", resp.StatusCode, env.Error)
	}
}

func TestPanicBecomesInternal(t *testing.T) {
	h := newHarness(t, 4)
	resp, env := h.do(t, "GET", "/v1/panic", ``, nil)
	if resp.StatusCode != 500 || env.Error.Code != protocol.CodeInternal {
		t.Fatalf("panic: %d %+v", resp.StatusCode, env.Error)
	}
	if _, env := h.do(t, "POST", "/v1/read", `{}`, nil); !env.OK {
		t.Fatal("server unusable after panic")
	}
}

func TestAuditOneRecordPerRequest(t *testing.T) {
	h := newHarness(t, 4)
	h.do(t, "POST", "/v1/act", `{}`, map[string]string{protocol.HeaderRequestID: rid})
	h.do(t, "GET", "/v1/nope", ``, nil)
	h.do(t, "POST", "/v1/act", `{}`, map[string]string{protocol.HeaderRequestID: "bad"})
	events := h.audit.List()
	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}
	first := events[0]
	if first.Kind != "request" || first.RequestID != rid || first.Action != "test.act" || first.Code != "ok" || first.Params["n"] != "x" || first.Remote == "" {
		t.Fatalf("first event = %+v", first)
	}
	if events[1].Code != "not_found" || events[2].Code != "invalid_request" {
		t.Fatalf("codes = %q %q", events[1].Code, events[2].Code)
	}
}

func TestPassthroughAuthenticationSlot(t *testing.T) {
	var seen atomic.Int64
	srv := New(Options{Authenticate: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen.Add(1); next.ServeHTTP(w, r) })
	}}, []*Route{{Method: "GET", Path: "/v1/x", Action: "x", Handler: func(*Call) (any, error) { return nil, nil }}})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/x", nil))
	if rec.Code != 200 || seen.Load() != 1 {
		t.Fatalf("auth slot not in chain: code=%d seen=%d", rec.Code, seen.Load())
	}
	if got := Passthrough(http.NotFoundHandler()); got == nil {
		t.Fatal("passthrough returned nil")
	}
}

func TestClientDisconnectCancelsContext(t *testing.T) {
	cancelled := make(chan error, 1)
	h := newHarness(t, 4, &Route{Method: "POST", Path: "/v1/hold", Action: "hold", Class: NonRetryable, Handler: func(c *Call) (any, error) {
		// Go detects a closed connection only once the body has been read.
		if err := c.Decode(&struct{}{}); err != nil {
			return nil, err
		}
		ctx := c.Context(time.Minute)
		<-ctx.Done()
		cancelled <- ctx.Err()
		return nil, ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", h.http.URL+"/v1/hold", strings.NewReader(`{}`))
	req.Header.Set(protocol.HeaderRequestID, rid)
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, _ = http.DefaultClient.Do(req)
	select {
	case err := <-cancelled:
		if err != context.Canceled {
			t.Fatalf("ctx err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler context not cancelled on disconnect")
	}
	// The recorded outcome is replayed to a retry with the same id.
	var env envelope
	for i := 0; i < 100; i++ {
		var resp *http.Response
		resp, env = h.do(t, "POST", "/v1/hold", `{}`, map[string]string{protocol.HeaderRequestID: rid})
		if resp.Header.Get(protocol.HeaderDedupHit) == "true" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if env.Error == nil || env.Error.Details["reason"] != protocol.ReasonClientDisconnected {
		t.Fatalf("replay = %+v", env)
	}
}

func TestRoutesListing(t *testing.T) {
	h := newHarness(t, 4)
	routes := h.srv.Routes()
	if len(routes) != 6 || routes[0] != "GET /v1/panic" {
		t.Fatalf("routes = %v", routes)
	}
}
