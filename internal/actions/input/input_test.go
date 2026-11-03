package input

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// recorder is a Backend that logs every event.
type recorder struct {
	mu      sync.Mutex
	events  []string
	x, y    int
	failOn  string
	begins  int
	ends    int
	onEvent func(string)
}

func (r *recorder) log(format string, args ...any) error {
	r.mu.Lock()
	ev := fmt.Sprintf(format, args...)
	r.events = append(r.events, ev)
	fail := r.failOn != "" && strings.HasPrefix(ev, r.failOn)
	hook := r.onEvent
	r.mu.Unlock()
	if hook != nil {
		hook(ev)
	}
	if fail {
		return errors.New("injected failure")
	}
	return nil
}

func (r *recorder) Begin() error { r.begins++; return nil }
func (r *recorder) End()         { r.ends++ }
func (r *recorder) Move(x, y int) error {
	r.mu.Lock()
	r.x, r.y = x, y
	r.mu.Unlock()
	return r.log("move %d,%d", x, y)
}
func (r *recorder) Position() (int, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.x, r.y, nil
}
func (r *recorder) Button(b Button, down bool) error {
	return r.log("%s %s", b, map[bool]string{true: "down", false: "up"}[down])
}
func (r *recorder) Scroll(dx, dy int) error { return r.log("scroll %d,%d", dx, dy) }
func (r *recorder) Key(k Key, down bool) error {
	return r.log("key %s %s", k.Name, map[bool]string{true: "down", false: "up"}[down])
}
func (r *recorder) TypeUnicode(c rune) error  { return r.log("type %c", c) }
func (r *recorder) TypeScancode(c rune) error { return r.log("scan %c", c) }

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func newTestService() (*Service, *recorder) {
	rec := &recorder{}
	s := NewService(rec)
	s.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() } // no real waiting
	return s, rec
}

func run(t *testing.T, s *Service, typ, body string) error {
	t.Helper()
	act, err := Compile(typ, []byte(body), false)
	if err != nil {
		return err
	}
	return s.Run(context.Background(), func(ctx context.Context, x *Exec) error { return act.Run(ctx, x) })
}

func equal(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("events:\n got %q\nwant %q", got, want)
	}
}

func TestMoveInterpolation(t *testing.T) {
	s, rec := newTestService()
	if err := run(t, s, TypeMouseMove, `{"x":0,"y":0}`); err != nil {
		t.Fatal(err)
	}
	if err := run(t, s, TypeMouseMove, `{"x":100,"y":50,"duration_ms":100}`); err != nil {
		t.Fatal(err)
	}
	ev := rec.got()
	// 100 ms / 8 ms = 12 steps; 111.8 px / 2 px = 55 steps; the smaller wins.
	if len(ev) != 1+12 || ev[len(ev)-1] != "move 100,50" {
		t.Fatalf("events = %q", ev)
	}
	// Short distances are limited by the 2 pixel minimum step.
	s, rec = newTestService()
	run(t, s, TypeMouseMove, `{"x":0,"y":0}`)
	run(t, s, TypeMouseMove, `{"x":5,"y":0,"duration_ms":1000}`)
	if ev := rec.got(); len(ev) != 1+2 {
		t.Fatalf("short move events = %q", ev)
	}
	if err := run(t, s, TypeMouseMove, `{"x":1}`); err == nil {
		t.Fatal("missing y accepted")
	}
}

func TestClickVariants(t *testing.T) {
	s, rec := newTestService()
	if err := run(t, s, TypeMouseClick, `{"x":0,"y":0,"button":"right","count":2}`); err != nil {
		t.Fatal(err)
	}
	equal(t, rec.got(), "move 0,0", "right down", "right up", "right down", "right up")
	s, rec = newTestService()
	run(t, s, TypeMouseClick, `{}`)
	equal(t, rec.got(), "left down", "left up")
	for _, bad := range []string{`{"count":6}`, `{"count":0}`, `{"x":1}`, `{"button":"side"}`, `{"bogus":1}`} {
		if err := run(t, s, TypeMouseClick, bad); err == nil {
			t.Errorf("click %s accepted", bad)
		}
	}
}

func TestDragSequenceAndDefaultDuration(t *testing.T) {
	s, rec := newTestService()
	act, err := Compile(TypeMouseDrag, []byte(`{"from":{"x":0,"y":0},"to":{"x":20,"y":0}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if act.Expected != 300*time.Millisecond || act.DefaultTimeout() != 5300*time.Millisecond {
		t.Fatalf("drag default = %s", act.Expected)
	}
	s.Run(context.Background(), func(ctx context.Context, x *Exec) error { return act.Run(ctx, x) })
	ev := rec.got()
	if ev[0] != "move 0,0" || ev[1] != "left down" || ev[len(ev)-2] != "move 20,0" || ev[len(ev)-1] != "left up" {
		t.Fatalf("drag events = %q", ev)
	}
}

func TestCancelledDragReleasesButton(t *testing.T) {
	rec := &recorder{}
	s := NewService(rec)
	ctx, cancel := context.WithCancel(context.Background())
	rec.onEvent = func(ev string) {
		if ev == "left down" {
			cancel()
		}
	}
	act, _ := Compile(TypeMouseDrag, []byte(`{"from":{"x":0,"y":0},"to":{"x":500,"y":0},"duration_ms":2000}`), false)
	err := s.Run(ctx, func(ctx context.Context, x *Exec) error { return act.Run(ctx, x) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	ev := rec.got()
	if ev[len(ev)-1] != "left up" {
		t.Fatalf("button left down after cancellation: %q", ev)
	}
}

func TestExplicitDownStaysDown(t *testing.T) {
	s, rec := newTestService()
	run(t, s, TypeMouseDown, `{"button":"middle"}`)
	run(t, s, TypeKeyDown, `{"key":"shift"}`)
	equal(t, rec.got(), "middle down", "key shift down")
}

func TestHotkeyOrder(t *testing.T) {
	s, rec := newTestService()
	if err := run(t, s, TypeKeyHotkey, `{"keys":["Ctrl","shift","esc"]}`); err != nil {
		t.Fatal(err)
	}
	equal(t, rec.got(), "key ctrl down", "key shift down", "key escape down", "key escape up", "key shift up", "key ctrl up")
	err := run(t, s, TypeKeyHotkey, `{"keys":["ctrl","nosuchkey"]}`)
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Details["reason"] != protocol.ReasonUnknownKey {
		t.Fatalf("unknown key err = %v", err)
	}
}

func TestHotkeyFailureReleasesPressedKeys(t *testing.T) {
	s, rec := newTestService()
	rec.failOn = "key escape down"
	if err := run(t, s, TypeKeyHotkey, `{"keys":["ctrl","alt","escape"]}`); err == nil {
		t.Fatal("expected failure")
	}
	equal(t, rec.got(), "key ctrl down", "key alt down", "key escape down", "key alt up", "key ctrl up")
}

func TestTypeTextSpecialCharacters(t *testing.T) {
	s, rec := newTestService()
	if err := run(t, s, TypeKeyType, `{"text":"aé\r\nb\t好"}`); err != nil {
		t.Fatal(err)
	}
	equal(t, rec.got(), "type a", "type é", "key enter down", "key enter up", "type b", "key tab down", "key tab up", "type 好")
	s, rec = newTestService()
	if err := run(t, s, TypeKeyType, `{"text":"Ab","method":"scancode"}`); err != nil {
		t.Fatal(err)
	}
	equal(t, rec.got(), "scan A", "scan b")
	if err := run(t, s, TypeKeyType, `{"text":"é","method":"scancode"}`); err == nil {
		t.Fatal("scancode accepted non-ASCII")
	}
	act, _ := Compile(TypeKeyType, []byte(`{"text":"abcd","interval_ms":100}`), false)
	if act.Expected != 300*time.Millisecond || act.Params["chars"] != "4" || act.Params["text"] != "" {
		t.Fatalf("type action = %+v", act)
	}
	if v, _ := Compile(TypeKeyType, []byte(`{"text":"secret"}`), true); v.Params["text"] != "secret" {
		t.Fatal("verbose audit must record text")
	}
}

func TestScroll(t *testing.T) {
	s, rec := newTestService()
	run(t, s, TypeMouseScroll, `{"dy":-3,"dx":1}`)
	equal(t, rec.got(), "scroll 1,-3")
	if err := run(t, s, TypeMouseScroll, `{}`); err == nil {
		t.Fatal("zero scroll accepted")
	}
}

func TestWaitOnlyInBatch(t *testing.T) {
	if _, err := Compile(TypeWait, []byte(`{"ms":5}`), false); err == nil {
		t.Fatal("wait accepted outside batch")
	}
}

func batchOf(t *testing.T, items ...string) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, len(items))
	for i, it := range items {
		out[i] = json.RawMessage(it)
	}
	return out
}

func TestBatchRunsInOrder(t *testing.T) {
	var items []string
	var want []string
	for i := 0; i < 50; i++ {
		items = append(items, fmt.Sprintf(`{"type":"mouse.move","x":%d,"y":%d,"delay_after_ms":1}`, i, i))
		want = append(want, fmt.Sprintf("move %d,%d", i, i))
	}
	b, err := CompileBatch(batchOf(t, items...), false)
	if err != nil {
		t.Fatal(err)
	}
	s, rec := newTestService()
	var n int
	err = s.Run(context.Background(), func(ctx context.Context, x *Exec) error {
		var err error
		n, err = b.Run(ctx, x)
		return err
	})
	if err != nil || n != 50 {
		t.Fatalf("executed %d err %v", n, err)
	}
	equal(t, rec.got(), want...)
}

func TestBatchStopsAtFirstFailure(t *testing.T) {
	b, err := CompileBatch(batchOf(t,
		`{"type":"key.press","key":"a"}`,
		`{"type":"wait","ms":10}`,
		`{"type":"mouse.click","x":1,"y":2}`,
		`{"type":"key.press","key":"b"}`,
	), false)
	if err != nil {
		t.Fatal(err)
	}
	s, rec := newTestService()
	rec.failOn = "left down"
	var n int
	err = s.Run(context.Background(), func(ctx context.Context, x *Exec) error {
		var err error
		n, err = b.Run(ctx, x)
		return err
	})
	if err == nil || n != 2 {
		t.Fatalf("executed = %d, err = %v; want 2 and an error", n, err)
	}
	for _, ev := range rec.got() {
		if ev == "key b down" {
			t.Fatal("batch continued after the failure")
		}
	}
}

func TestBatchValidatesEverythingFirst(t *testing.T) {
	_, err := CompileBatch(batchOf(t, `{"type":"key.press","key":"a"}`, `{"type":"mouse.teleport"}`), false)
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Details["index"] != 1 || perr.Details["executed"] != 0 {
		t.Fatalf("err = %v", err)
	}
	_, err = CompileBatch(batchOf(t, `{"type":"key.press","key":"a","text":"x"}`), false)
	if err == nil {
		t.Fatal("field of another action type accepted")
	}
	if _, err := CompileBatch(nil, false); err == nil {
		t.Fatal("empty batch accepted")
	}
	big := make([]string, MaxBatchActions+1)
	for i := range big {
		big[i] = `{"type":"key.press","key":"a"}`
	}
	if _, err := CompileBatch(batchOf(t, big...), false); err == nil {
		t.Fatal("201 actions accepted")
	}
}

func TestBatchDefaultTimeout(t *testing.T) {
	b, _ := CompileBatch(batchOf(t, `{"type":"mouse.move","x":1,"y":1,"duration_ms":1000}`, `{"type":"wait","ms":500,"delay_after_ms":250}`), false)
	// (5 s + 1 s) + (5 s + 0.5 s + 0.25 s) + 5 s
	if got := b.DefaultTimeout(); got != 16750*time.Millisecond {
		t.Fatalf("default timeout = %s", got)
	}
	many := make([]string, 20)
	for i := range many {
		many[i] = `{"type":"key.press","key":"a"}`
	}
	b, _ = CompileBatch(batchOf(t, many...), false)
	if got := b.DefaultTimeout(); got != BatchTimeoutCap {
		t.Fatalf("capped default = %s", got)
	}
}

func TestQueueSerialisesAndExpiredRequestsNeverRun(t *testing.T) {
	s, rec := newTestService()
	release := make(chan struct{})
	started := make(chan struct{})
	go s.Run(context.Background(), func(ctx context.Context, x *Exec) error {
		close(started)
		<-release
		return nil
	})
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	ran := false
	err := s.Run(ctx, func(ctx context.Context, x *Exec) error { ran = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || ran {
		t.Fatalf("queued request err = %v ran = %v", err, ran)
	}
	close(release)
	if err := s.Run(context.Background(), func(ctx context.Context, x *Exec) error { return x.Press(mustKey("a")) }); err != nil {
		t.Fatal(err)
	}
	if rec.begins != 2 || rec.ends != 2 {
		t.Fatalf("Begin/End = %d/%d", rec.begins, rec.ends)
	}
}

func TestKeyLookup(t *testing.T) {
	cases := map[string]string{"A": "a", "Return": "enter", "ESC": "escape", "super": "win", "-": "minus", "F24": "f24", "NumPad5": "numpad5", "PgDn": "pagedown"}
	for in, want := range cases {
		k, ok := LookupKey(in)
		if !ok || k.Name != want {
			t.Errorf("LookupKey(%q) = %+v, %v; want %s", in, k, ok, want)
		}
	}
	if k, _ := LookupKey("f12"); k.VK != 0x7B || k.Keysym != 0xffc9 {
		t.Errorf("f12 = %+v", k)
	}
	if k, _ := LookupKey("up"); !k.Ext || k.VK != 0x26 || k.Keysym != 0xff52 {
		t.Errorf("up = %+v", k)
	}
	for _, name := range KeyNames() {
		k := keyTable[name]
		if k.VK == 0 || k.Keysym == 0 || k.Scan == 0 {
			t.Errorf("incomplete key %+v", k)
		}
	}
	for alias, target := range keyAliases {
		if _, ok := keyTable[target]; !ok {
			t.Errorf("alias %q points at missing key %q", alias, target)
		}
		if _, clash := keyTable[alias]; clash {
			t.Errorf("alias %q shadows a key", alias)
		}
	}
}
