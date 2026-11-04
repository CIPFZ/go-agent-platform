// Package a11y implements /v1/a11y/tree (spec 6.9): the accessibility tree
// of a window, collected in a time-limited child process so a hung
// application can never block vm-agent. On both platforms vm-agent
// re-executes itself as the helper: Windows walks the native UI Automation
// COM interfaces, Linux walks AT-SPI over D-Bus.
package a11y

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/CIPFZ/go-agent-platform/internal/actions/process"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Node is one accessibility element. Rect is in API coordinates (physical
// pixels from the primary display's corner); nil when the element has no
// on-screen extent.
type Node struct {
	Role     string         `json:"role"`
	Name     string         `json:"name"`
	Value    string         `json:"value,omitempty"`
	Rect     *protocol.Rect `json:"rect"`
	State    []string       `json:"state"`
	Children []*Node        `json:"children"`
}

// Defaults and caps of the request (spec 6.9 gives the defaults).
const (
	DefaultMaxDepth = 10
	DefaultMaxNodes = 5000
	MaxDepthCap     = 100
	MaxNodesCap     = 50000
	maxValueRunes   = 4096
	maxOutputBytes  = 64 << 20
)

// Target is the window whose tree is collected, resolved by the caller.
type Target struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	PID   int    `json:"pid"`
}

// Request is passed to the helper process.
type Request struct {
	Target   Target `json:"target"`
	MaxDepth int    `json:"max_depth"`
	MaxNodes int    `json:"max_nodes"`
	// OriginX and OriginY translate screen coordinates to API coordinates.
	OriginX int `json:"origin_x"`
	OriginY int `json:"origin_y"`
}

// Result is the a11y/tree result.
type Result struct {
	Window    Target `json:"window"`
	Root      *Node  `json:"root"`
	Nodes     int    `json:"nodes"`
	Truncated bool   `json:"truncated"`
}

// helperOutput is what a helper prints: a result or an error.
type helperOutput struct {
	Result *Result `json:"result,omitempty"`
	Error  *struct {
		Code    protocol.Code `json:"code"`
		Message string        `json:"message"`
	} `json:"error,omitempty"`
}

// collector walks the tree; Windows and Linux differ.
type collector interface {
	command(req Request) (*exec.Cmd, error)
}

// Collect runs the platform helper for req under ctx and parses its output.
// When ctx ends the helper's process tree is killed and ctx.Err() returned.
func Collect(ctx context.Context, req Request) (*Result, error) {
	cmd, err := platform.command(req)
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitWriter{w: &stdout, left: maxOutputBytes}
	cmd.Stderr = &limitWriter{w: &stderr, left: 64 << 10}
	tree, err := process.StartTree(cmd)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = tree.Kill() })
	waitErr := tree.Wait()
	stop()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var out helperOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || (out.Result == nil && out.Error == nil) {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = fmt.Sprint(waitErr)
		}
		return nil, protocol.Errorf(protocol.CodeIOFailed, "accessibility helper failed: %s", firstLine(msg))
	}
	if out.Error != nil {
		return nil, protocol.Errorf(out.Error.Code, "%s", out.Error.Message)
	}
	out.Result.Window = req.Target
	return out.Result, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

type limitWriter struct {
	w    *bytes.Buffer
	left int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > l.left {
		p = p[:l.left]
	}
	l.w.Write(p)
	l.left -= len(p)
	return n, nil
}

// helperEnv marks a vm-agent process started as the accessibility helper;
// its value is the JSON request.
const helperEnv = "VM_AGENT_A11Y_HELPER"

// helperCommand re-executes vm-agent as the helper for req.
func helperCommand(req Request) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), helperEnv+"="+string(data))
	return cmd, nil
}

// IsHelper reports whether this process was started as the helper.
func IsHelper() bool { return os.Getenv(helperEnv) != "" }

// HelperMain runs the helper: it reads the request from the environment,
// walks the tree and prints the JSON output. It returns the exit code.
func HelperMain() int {
	var req Request
	out := helperOutput{}
	if err := json.Unmarshal([]byte(os.Getenv(helperEnv)), &req); err != nil {
		out.Error = &struct {
			Code    protocol.Code `json:"code"`
			Message string        `json:"message"`
		}{protocol.CodeInternal, "bad helper request: " + err.Error()}
	} else if res, err := walk(req); err != nil {
		code, msg := protocol.CodeIOFailed, err.Error()
		var perr *protocol.Error
		if errors.As(err, &perr) {
			code, msg = perr.Code, perr.Message
		}
		out.Error = &struct {
			Code    protocol.Code `json:"code"`
			Message string        `json:"message"`
		}{code, msg}
	} else {
		out.Result = res
	}
	data, _ := json.Marshal(out)
	os.Stdout.Write(data)
	return 0
}

// budget tracks the node and depth limits while walking.
type budget struct {
	maxDepth, maxNodes, nodes int
	truncated                 bool
}

func (b *budget) take() bool {
	if b.nodes >= b.maxNodes {
		b.truncated = true
		return false
	}
	b.nodes++
	return true
}

// clip limits text values.
func clip(s string) string {
	if r := []rune(s); len(r) > maxValueRunes {
		return string(r[:maxValueRunes])
	}
	return s
}

// snake converts platform role names ("push button", "ListItem") to
// snake_case ("push_button", "list_item").
func snake(s string) string {
	var b strings.Builder
	prevLower := false
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r == ' ' || r == '-' || r == '_':
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
				b.WriteByte('_')
			}
			prevLower = false
		case r >= 'A' && r <= 'Z':
			if prevLower {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
			prevLower = false
		default:
			b.WriteRune(r)
			prevLower = true
		}
	}
	return b.String()
}
