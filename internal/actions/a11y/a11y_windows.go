//go:build windows

package a11y

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/CIPFZ/go-agent-platform/internal/platform/win"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

var platform collector = uia{}

type uia struct{}

// command re-executes vm-agent as the helper, which walks the tree through
// the native UI Automation COM interfaces.
func (uia) command(req Request) (*exec.Cmd, error) { return helperCommand(req) }

// Available reports whether accessibility trees can be collected: the
// native UI Automation core ships with every supported Windows version.
func Available() bool {
	_, err := os.Stat(filepath.Join(os.Getenv("SystemRoot"), "System32", "UIAutomationCore.dll"))
	return err == nil
}

// props are cached on every element, so reading one costs a single
// cross-process call.
var props = []int{
	win.UIABoundingRectangle, win.UIAControlType, win.UIAName,
	win.UIAHasKeyboardFocus, win.UIAIsKeyboardFocusable, win.UIAIsEnabled, win.UIAIsOffscreen,
	win.UIAIsValueAvailable, win.UIAValueValue, win.UIAValueIsReadOnly,
	win.UIAIsRangeValueAvailable, win.UIARangeValueValue,
	win.UIAIsToggleAvailable, win.UIAToggleState,
	win.UIAIsSelectionItemAvailable, win.UIASelectionItemIsSelected,
	win.UIAIsExpandCollapseAvailable, win.UIAExpandCollapseState,
	win.UIAIsWindowAvailable, win.UIAWindowIsModal,
}

// walk collects the control view of the target window.
func walk(req Request) (*Result, error) {
	runtime.LockOSThread()
	// Per-Monitor V2 so bounding rectangles are physical pixels (spec D1).
	win.InitDPIAwareness()
	id, err := strconv.ParseUint(strings.TrimPrefix(req.Target.ID, "0x"), 16, 64)
	if err != nil || !win.IsWindow(win.HWND(id)) {
		return nil, protocol.Errorf(protocol.CodeNotFound, "window %s does not exist", req.Target.ID)
	}
	u, err := win.NewUIA(props)
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeIOFailed, "UI Automation: %v", err)
	}
	defer u.Close()
	root, err := u.ElementFromHandle(win.HWND(id))
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeIOFailed, "UI Automation: %v", err)
	}
	defer root.Release()
	w := &uiaWalker{u: u, req: req, b: budget{maxDepth: req.MaxDepth, maxNodes: req.MaxNodes}}
	w.b.take()
	tree := w.node(root, 1)
	return &Result{Root: tree, Nodes: w.b.nodes, Truncated: w.b.truncated}, nil
}

type uiaWalker struct {
	u   *win.UIA
	req Request
	b   budget
}

func (w *uiaWalker) node(e *win.UIAElement, depth int) *Node {
	n := &Node{Role: controlRole(e.Prop(win.UIAControlType)), Name: str(e.Prop(win.UIAName)), State: []string{}, Children: []*Node{}}
	if r, ok := e.Prop(win.UIABoundingRectangle).([]float64); ok && len(r) == 4 && r[2] > 0 && r[3] > 0 {
		n.Rect = &protocol.Rect{X: int(math.Round(r[0])) - w.req.OriginX, Y: int(math.Round(r[1])) - w.req.OriginY, W: int(math.Round(r[2])), H: int(math.Round(r[3]))}
	}
	state := func(name string, on bool) {
		if on {
			n.State = append(n.State, name)
		}
	}
	state("enabled", flag(e.Prop(win.UIAIsEnabled)))
	state("focusable", flag(e.Prop(win.UIAIsKeyboardFocusable)))
	state("focused", flag(e.Prop(win.UIAHasKeyboardFocus)))
	state("offscreen", flag(e.Prop(win.UIAIsOffscreen)))
	switch {
	case flag(e.Prop(win.UIAIsValueAvailable)):
		n.Value = clip(str(e.Prop(win.UIAValueValue)))
		readOnly := flag(e.Prop(win.UIAValueIsReadOnly))
		state("read_only", readOnly)
		state("editable", !readOnly)
	case flag(e.Prop(win.UIAIsRangeValueAvailable)):
		if v, ok := e.Prop(win.UIARangeValueValue).(float64); ok {
			n.Value = strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	if flag(e.Prop(win.UIAIsToggleAvailable)) {
		state("checked", e.Prop(win.UIAToggleState) == int32(1)) // ToggleState_On
	}
	if flag(e.Prop(win.UIAIsSelectionItemAvailable)) {
		state("selected", flag(e.Prop(win.UIASelectionItemIsSelected)))
	}
	if flag(e.Prop(win.UIAIsExpandCollapseAvailable)) {
		s := e.Prop(win.UIAExpandCollapseState)
		state("collapsed", s == int32(0))
		state("expanded", s == int32(1))
	}
	if flag(e.Prop(win.UIAIsWindowAvailable)) {
		state("modal", flag(e.Prop(win.UIAWindowIsModal)))
	}
	sort.Strings(n.State)

	child, err := w.u.FirstChild(e)
	if err != nil || child == nil {
		return n
	}
	if depth >= w.b.maxDepth {
		child.Release()
		w.b.truncated = true
		return n
	}
	for child != nil {
		if !w.b.take() {
			child.Release()
			break
		}
		n.Children = append(n.Children, w.node(child, depth+1))
		next, err := w.u.NextSibling(child)
		child.Release()
		if err != nil {
			break
		}
		child = next
	}
	return n
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func flag(v any) bool {
	b, _ := v.(bool)
	return b
}

// controlRoles are the UIA control type ids (50000 + index) in snake_case,
// the names the managed ControlType.ProgrammaticName gives.
var controlRoles = []string{
	"button", "calendar", "check_box", "combo_box", "edit", "hyperlink", "image", "list_item", "list", "menu",
	"menu_bar", "menu_item", "progress_bar", "radio_button", "scroll_bar", "slider", "spinner", "status_bar", "tab", "tab_item",
	"text", "tool_bar", "tool_tip", "tree", "tree_item", "custom", "group", "thumb", "data_grid", "data_item",
	"document", "split_button", "window", "pane", "header", "header_item", "table", "title_bar", "separator", "semantic_zoom",
	"app_bar",
}

func controlRole(v any) string {
	if id, ok := v.(int32); ok && id >= 50000 && int(id-50000) < len(controlRoles) {
		return controlRoles[id-50000]
	}
	return "custom"
}
