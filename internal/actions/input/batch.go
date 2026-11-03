package input

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Action is one validated input action, ready to run.
type Action struct {
	Type string
	// Expected is how long the action takes by design (duration_ms, typing
	// time, wait); its default timeout is BaseTimeout plus Expected.
	Expected time.Duration
	// DelayAfter is the batch delay_after_ms.
	DelayAfter time.Duration
	// Params is the audit summary (spec section 8).
	Params map[string]string
	run    func(ctx context.Context, x *Exec) error
}

// Run executes the action.
func (a *Action) Run(ctx context.Context, x *Exec) error { return a.run(ctx, x) }

// DefaultTimeout is the spec section 7 default for a single action.
func (a *Action) DefaultTimeout() time.Duration { return BaseTimeout + a.Expected }

// Action type names, shared by the batch "type" field.
const (
	TypeMouseMove   = "mouse.move"
	TypeMouseClick  = "mouse.click"
	TypeMouseDown   = "mouse.down"
	TypeMouseUp     = "mouse.up"
	TypeMouseDrag   = "mouse.drag"
	TypeMouseScroll = "mouse.scroll"
	TypeKeyType     = "key.type"
	TypeKeyPress    = "key.press"
	TypeKeyDown     = "key.down"
	TypeKeyUp       = "key.up"
	TypeKeyHotkey   = "key.hotkey"
	TypeWait        = "wait"
)

func strictDecode(raw []byte, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid parameters: %v", err).WithReason(protocol.ReasonInvalidJSON)
	}
	return nil
}

func invalid(format string, args ...any) *protocol.Error {
	return protocol.Errorf(protocol.CodeInvalidRequest, format, args...)
}

func durationMS(ms *int, def int, field string) (time.Duration, error) {
	v := def
	if ms != nil {
		v = *ms
	}
	if v < 0 {
		return 0, invalid("%s must not be negative", field)
	}
	return time.Duration(v) * time.Millisecond, nil
}

// Compile validates the parameters of one single-action request of type typ;
// raw is the request body.
func Compile(typ string, raw []byte, verbose bool) (*Action, error) {
	return compile(typ, raw, verbose, false)
}

type batchFields struct {
	Type         string `json:"type"`
	DelayAfterMS *int   `json:"delay_after_ms"`
}

func compile(typ string, raw []byte, verbose, inBatch bool) (*Action, error) {
	act := &Action{Type: typ, Params: map[string]string{}}
	var common batchFields
	decodeInto := func(v any) error {
		if inBatch {
			return decodeWithBatchFields(raw, v, &common)
		}
		return strictDecode(raw, v)
	}
	point := func(p *protocol.Point) string { return strconv.Itoa(p.X) + "," + strconv.Itoa(p.Y) }
	switch typ {
	case TypeMouseMove:
		var req protocol.MouseMoveRequest
		if err := decodeInto(&req); err != nil {
			return nil, err
		}
		if req.X == nil || req.Y == nil {
			return nil, invalid("x and y are required")
		}
		d, err := durationMS(req.DurationMS, 0, "duration_ms")
		if err != nil {
			return nil, err
		}
		to := protocol.Point{X: *req.X, Y: *req.Y}
		act.Expected = d
		act.Params["to"] = point(&to)
		act.run = func(ctx context.Context, x *Exec) error { return x.MoveTo(ctx, to, d) }
	case TypeMouseClick:
		var req protocol.MouseClickRequest
		if err := decodeInto(&req); err != nil {
			return nil, err
		}
		btn, err := ParseButton(req.Button)
		if err != nil {
			return nil, err
		}
		count := 1
		if req.Count != nil {
			count = *req.Count
		}
		if count < 1 || count > MaxClickCount {
			return nil, invalid("count must be between 1 and %d", MaxClickCount)
		}
		if (req.X == nil) != (req.Y == nil) {
			return nil, invalid("x and y must be given together")
		}
		var at *protocol.Point
		if req.X != nil {
			at = &protocol.Point{X: *req.X, Y: *req.Y}
			act.Params["at"] = point(at)
		}
		act.Params["button"], act.Params["count"] = string(btn), strconv.Itoa(count)
		act.run = func(ctx context.Context, x *Exec) error { return x.Click(ctx, at, btn, count) }
	case TypeMouseDown, TypeMouseUp:
		var req protocol.MouseButtonRequest
		if err := decodeInto(&req); err != nil {
			return nil, err
		}
		btn, err := ParseButton(req.Button)
		if err != nil {
			return nil, err
		}
		down := typ == TypeMouseDown
		act.Params["button"] = string(btn)
		act.run = func(ctx context.Context, x *Exec) error { return x.ButtonState(btn, down) }
	case TypeMouseDrag:
		var req protocol.MouseDragRequest
		if err := decodeInto(&req); err != nil {
			return nil, err
		}
		if req.From == nil || req.To == nil {
			return nil, invalid("from and to are required")
		}
		btn, err := ParseButton(req.Button)
		if err != nil {
			return nil, err
		}
		d, err := durationMS(req.DurationMS, DefaultDragMS, "duration_ms")
		if err != nil {
			return nil, err
		}
		from, to := *req.From, *req.To
		act.Expected = d
		act.Params["from"], act.Params["to"], act.Params["button"] = point(&from), point(&to), string(btn)
		act.run = func(ctx context.Context, x *Exec) error { return x.Drag(ctx, from, to, btn, d) }
	case TypeMouseScroll:
		var req protocol.MouseScrollRequest
		if err := decodeInto(&req); err != nil {
			return nil, err
		}
		if req.DX == 0 && req.DY == 0 {
			return nil, invalid("dx or dy must be non-zero")
		}
		if abs(req.DX) > 1000 || abs(req.DY) > 1000 {
			return nil, invalid("dx and dy are limited to 1000 ticks")
		}
		dx, dy := req.DX, req.DY
		act.Params["dx"], act.Params["dy"] = strconv.Itoa(dx), strconv.Itoa(dy)
		act.run = func(ctx context.Context, x *Exec) error { return x.Scroll(dx, dy) }
	case TypeKeyType:
		var req protocol.KeyTypeRequest
		if err := decodeInto(&req); err != nil {
			return nil, err
		}
		if !utf8.ValidString(req.Text) {
			return nil, invalid("text must be valid UTF-8")
		}
		interval, err := durationMS(req.IntervalMS, DefaultTypeMS, "interval_ms")
		if err != nil {
			return nil, err
		}
		var scancode bool
		switch req.Method {
		case "", "unicode":
		case "scancode":
			scancode = true
		default:
			return nil, invalid("method must be unicode or scancode")
		}
		text := req.Text
		act.Expected = TypeDuration(text, interval)
		act.Params["chars"] = strconv.Itoa(utf8.RuneCountInString(text))
		if req.Method != "" {
			act.Params["method"] = req.Method
		}
		if verbose {
			act.Params["text"] = text
		}
		act.run = func(ctx context.Context, x *Exec) error { return x.Type(ctx, text, interval, scancode) }
	case TypeKeyPress, TypeKeyDown, TypeKeyUp:
		var req protocol.KeyRequest
		if err := decodeInto(&req); err != nil {
			return nil, err
		}
		k, err := ResolveKey(req.Key)
		if err != nil {
			return nil, err
		}
		act.Params["key"] = k.Name
		switch typ {
		case TypeKeyPress:
			act.run = func(ctx context.Context, x *Exec) error { return x.Press(k) }
		default:
			down := typ == TypeKeyDown
			act.run = func(ctx context.Context, x *Exec) error { return x.KeyState(k, down) }
		}
	case TypeKeyHotkey:
		var req protocol.HotkeyRequest
		if err := decodeInto(&req); err != nil {
			return nil, err
		}
		if len(req.Keys) == 0 || len(req.Keys) > 8 {
			return nil, invalid("keys must list 1 to 8 keys")
		}
		keys := make([]Key, len(req.Keys))
		names := make([]string, len(req.Keys))
		for i, name := range req.Keys {
			k, err := ResolveKey(name)
			if err != nil {
				return nil, err
			}
			keys[i], names[i] = k, k.Name
		}
		act.Params["keys"] = strings.Join(names, "+")
		act.run = func(ctx context.Context, x *Exec) error { return x.Hotkey(ctx, keys) }
	case TypeWait:
		if !inBatch {
			return nil, invalid("wait is only valid inside input/batch")
		}
		var req protocol.WaitRequest
		if err := decodeInto(&req); err != nil {
			return nil, err
		}
		if req.MS == nil || *req.MS < 0 {
			return nil, invalid("wait requires a non-negative ms")
		}
		d := time.Duration(*req.MS) * time.Millisecond
		act.Expected = d
		act.Params["ms"] = strconv.Itoa(*req.MS)
		act.run = func(ctx context.Context, x *Exec) error { return x.Wait(ctx, d) }
	default:
		return nil, invalid("unknown action type %q", typ)
	}
	if inBatch {
		if common.DelayAfterMS != nil {
			if *common.DelayAfterMS < 0 {
				return nil, invalid("delay_after_ms must not be negative")
			}
			act.DelayAfter = time.Duration(*common.DelayAfterMS) * time.Millisecond
		}
	}
	return act, nil
}

// decodeWithBatchFields decodes raw into v while also accepting (and
// capturing) the batch-only "type" and "delay_after_ms" fields.
func decodeWithBatchFields(raw []byte, v any, common *batchFields) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid action: %v", err).WithReason(protocol.ReasonInvalidJSON)
	}
	if t, ok := fields["type"]; ok {
		_ = json.Unmarshal(t, &common.Type)
		delete(fields, "type")
	}
	if d, ok := fields["delay_after_ms"]; ok {
		var ms int
		if err := json.Unmarshal(d, &ms); err != nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "delay_after_ms must be an integer").WithReason(protocol.ReasonInvalidJSON)
		}
		common.DelayAfterMS = &ms
		delete(fields, "delay_after_ms")
	}
	rest, _ := json.Marshal(fields)
	return strictDecode(rest, v)
}

// Batch is a validated input/batch request.
type Batch struct {
	Actions []*Action
}

// CompileBatch validates every action before any of them runs, so a malformed
// batch executes nothing. The error carries details.index of the bad item.
func CompileBatch(items []json.RawMessage, verbose bool) (*Batch, error) {
	if len(items) == 0 {
		return nil, invalid("actions must not be empty")
	}
	if len(items) > MaxBatchActions {
		return nil, invalid("a batch holds at most %d actions", MaxBatchActions)
	}
	b := &Batch{}
	for i, raw := range items {
		var head batchFields
		if err := json.Unmarshal(raw, &head); err != nil || head.Type == "" {
			return nil, invalid("action %d needs a type", i).WithDetail("index", i).WithDetail("executed", 0)
		}
		act, err := compile(head.Type, raw, verbose, true)
		if err != nil {
			var perr *protocol.Error
			if errors.As(err, &perr) {
				return nil, perr.WithDetail("index", i).WithDetail("executed", 0)
			}
			return nil, err
		}
		b.Actions = append(b.Actions, act)
	}
	return b, nil
}

// DefaultTimeout is the sum of the actions' default timeouts plus 5 s, capped
// at 60 s (spec section 7).
func (b *Batch) DefaultTimeout() time.Duration {
	total := BaseTimeout
	for _, a := range b.Actions {
		total += a.DefaultTimeout() + a.DelayAfter
		if total >= BatchTimeoutCap {
			return BatchTimeoutCap
		}
	}
	return total
}

// Run executes the actions in order and stops at the first error, returning
// how many actions completed. Completed actions are not rolled back.
func (b *Batch) Run(ctx context.Context, x *Exec) (int, error) {
	for i, a := range b.Actions {
		if err := ctx.Err(); err != nil {
			return i, err
		}
		if err := a.Run(ctx, x); err != nil {
			return i, err
		}
		if a.DelayAfter > 0 {
			if err := x.Wait(ctx, a.DelayAfter); err != nil {
				// The action itself completed; only the pause was cut short.
				return i + 1, err
			}
		}
	}
	return len(b.Actions), nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
