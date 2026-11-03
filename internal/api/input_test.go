package api

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/input"
	"github.com/CIPFZ/go-agent-platform/internal/actions/session"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

type fakeInput struct {
	mu     sync.Mutex
	events []string
	failOn string
	delay  time.Duration
}

func (f *fakeInput) rec(ev string) error {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	if f.failOn != "" && ev == f.failOn {
		return errors.New("injected")
	}
	return nil
}

func (f *fakeInput) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *fakeInput) Begin() error                  { return nil }
func (f *fakeInput) End()                          {}
func (f *fakeInput) Move(x, y int) error           { return f.rec(fmt.Sprintf("move %d,%d", x, y)) }
func (f *fakeInput) Position() (int, int, error)   { return 0, 0, nil }
func (f *fakeInput) Scroll(dx, dy int) error       { return f.rec(fmt.Sprintf("scroll %d,%d", dx, dy)) }
func (f *fakeInput) TypeUnicode(r rune) error      { return f.rec("type " + string(r)) }
func (f *fakeInput) TypeScancode(r rune) error     { return f.rec("scan " + string(r)) }
func (f *fakeInput) Key(k input.Key, d bool) error { return f.rec(fmt.Sprintf("key %s %v", k.Name, d)) }
func (f *fakeInput) Button(b input.Button, d bool) error {
	return f.rec(fmt.Sprintf("%s %v", b, d))
}

func inputEnv(t *testing.T) (*testEnv, *fakeInput) {
	fi := &fakeInput{}
	return newEnv(t, func(d *Deps) { d.Input = fi }), fi
}

func TestDuplicateClickRequestIDClicksOnce(t *testing.T) {
	e, fi := inputEnv(t)
	body := `{"x":10,"y":20}`
	first := e.call("POST", "/v1/input/mouse/click", body, protocol.HeaderRequestID, "click-0000000000001")
	second := e.call("POST", "/v1/input/mouse/click", body, protocol.HeaderRequestID, "click-0000000000001")
	if !first.OK || !second.OK || second.Header.Get(protocol.HeaderDedupHit) != "true" {
		t.Fatalf("first=%s second=%s", first.Body, second.Body)
	}
	downs := 0
	for _, ev := range fi.list() {
		if ev == "left true" {
			downs++
		}
	}
	if downs != 1 {
		t.Fatalf("button pressed %d times: %q", downs, fi.list())
	}
	e.call("POST", "/v1/input/mouse/click", body, protocol.HeaderRequestID, "click-0000000000002")
	if n := strings.Count(strings.Join(fi.list(), "|"), "left true"); n != 2 {
		t.Fatalf("a new request id must click again: %d", n)
	}
}

func TestInputUpdatesLastInputAt(t *testing.T) {
	e, _ := inputEnv(t)
	e.call("POST", "/v1/input/key/press", `{"key":"enter"}`)
	var p protocol.ProbeResult
	e.call("GET", "/v1/probe", nil).into(t, &p)
	if p.LastInputAt == nil || time.Since(*p.LastInputAt) > time.Minute {
		t.Fatalf("last_input_at = %v", p.LastInputAt)
	}
}

func TestInputRefusedWhileLocked(t *testing.T) {
	e, fi := inputEnv(t)
	e.setSession(session.State{DisplayReady: true, Locked: true})
	e.call("POST", "/v1/input/key/type", `{"text":"x"}`).code(t, protocol.CodeNoDisplay)
	e.call("POST", "/v1/input/batch", `{"actions":[{"type":"key.press","key":"a"}]}`).code(t, protocol.CodeNoDisplay)
	if len(fi.list()) != 0 {
		t.Fatalf("input injected while locked: %q", fi.list())
	}
}

func TestBatchExecutedCountOnFailure(t *testing.T) {
	e, fi := inputEnv(t)
	var actions []string
	for i := 0; i < 50; i++ {
		actions = append(actions, fmt.Sprintf(`{"type":"mouse.move","x":%d,"y":0}`, i))
	}
	r := e.call("POST", "/v1/input/batch", `{"actions":[`+strings.Join(actions, ",")+`]}`)
	var res protocol.BatchResult
	r.into(t, &res)
	if res.Executed != 50 || len(fi.list()) != 50 || fi.list()[49] != "move 49,0" {
		t.Fatalf("executed=%d events=%d", res.Executed, len(fi.list()))
	}
	fi.failOn = "move 30,0"
	r = e.call("POST", "/v1/input/batch", `{"actions":[`+strings.Join(actions, ",")+`]}`)
	r.code(t, protocol.CodeIOFailed)
	if r.Error.Details["executed"] != float64(30) || r.Error.Details["index"] != float64(30) || r.Error.Details["type"] != "mouse.move" {
		t.Fatalf("details = %+v", r.Error.Details)
	}
	r = e.call("POST", "/v1/input/batch", `{"actions":[{"type":"key.press","key":"a"},{"type":"key.press","key":"nope"}]}`)
	r.code(t, protocol.CodeInvalidRequest)
	if r.Error.Details["executed"] != float64(0) || r.Error.Details["index"] != float64(1) {
		t.Fatalf("validation details = %+v", r.Error.Details)
	}
}

func TestBatchTimeoutReportsExecuted(t *testing.T) {
	e, fi := inputEnv(t)
	fi.delay = 30 * time.Millisecond
	var actions []string
	for i := 0; i < 20; i++ {
		actions = append(actions, `{"type":"key.press","key":"a"}`)
	}
	r := e.call("POST", "/v1/input/batch", `{"actions":[`+strings.Join(actions, ",")+`]}`, protocol.HeaderTimeoutMS, "200")
	r.code(t, protocol.CodeTimeout)
	executed := r.Error.Details["executed"].(float64)
	if executed < 1 || executed >= 20 || r.Error.Retryable {
		t.Fatalf("details = %+v retryable=%v", r.Error.Details, r.Error.Retryable)
	}
}

func TestInputValidation(t *testing.T) {
	e, _ := inputEnv(t)
	e.call("POST", "/v1/input/mouse/move", `{"x":1}`).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/input/key/press", `{"key":"hyper"}`).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/input/mouse/click", `{"count":9}`).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/input/key/type", `{"text":"x","colour":"red"}`).code(t, protocol.CodeInvalidRequest)
}

func TestKeyTypeAuditOmitsText(t *testing.T) {
	e, _ := inputEnv(t)
	e.call("POST", "/v1/input/key/type", `{"text":"hunter2"}`)
	ev := e.audit.List()
	last := ev[len(ev)-1]
	if last.Params["chars"] != "7" || strings.Contains(fmt.Sprint(last.Params), "hunter2") {
		t.Fatalf("audit params = %v", last.Params)
	}
}
