//go:build windows

package win

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Native UI Automation client (UIAutomationClient.h) called through COM
// vtables. Unlike the managed System.Windows.Automation client, the native
// core carries the proxies for standard Win32 controls (Edit, menus, title
// bars, status bars), so classic applications expose their real controls.

var (
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	oleaut32 = windows.NewLazySystemDLL("oleaut32.dll")

	procCoInitializeEx        = ole32.NewProc("CoInitializeEx")
	procCoUninitialize        = ole32.NewProc("CoUninitialize")
	procCoCreateInstance      = ole32.NewProc("CoCreateInstance")
	procVariantClear          = oleaut32.NewProc("VariantClear")
	procSysStringLen          = oleaut32.NewProc("SysStringLen")
	procSafeArrayGetUBound    = oleaut32.NewProc("SafeArrayGetUBound")
	procSafeArrayAccessData   = oleaut32.NewProc("SafeArrayAccessData")
	procSafeArrayUnaccessData = oleaut32.NewProc("SafeArrayUnaccessData")

	clsidCUIAutomation = windows.GUID{Data1: 0xff48dba4, Data2: 0x60ef, Data3: 0x4201, Data4: [8]byte{0xaa, 0x87, 0x54, 0x10, 0x3e, 0xef, 0x59, 0x4e}}
	iidIUIAutomation   = windows.GUID{Data1: 0x30cbe57d, Data2: 0xd9d0, Data3: 0x452a, Data4: [8]byte{0xab, 0x13, 0x7a, 0xc5, 0xac, 0x48, 0x25, 0xee}}
)

// UIA property ids (UIAutomationClient.h).
const (
	UIABoundingRectangle         = 30001
	UIAControlType               = 30003
	UIAName                      = 30005
	UIAHasKeyboardFocus          = 30008
	UIAIsKeyboardFocusable       = 30009
	UIAIsEnabled                 = 30010
	UIAIsOffscreen               = 30022
	UIAIsExpandCollapseAvailable = 30028
	UIAIsRangeValueAvailable     = 30033
	UIAIsSelectionItemAvailable  = 30036
	UIAIsToggleAvailable         = 30041
	UIAIsValueAvailable          = 30043
	UIAIsWindowAvailable         = 30044
	UIAValueValue                = 30045
	UIAValueIsReadOnly           = 30046
	UIARangeValueValue           = 30047
	UIAExpandCollapseState       = 30070
	UIAWindowIsModal             = 30077
	UIASelectionItemIsSelected   = 30079
	UIAToggleState               = 30086
)

// Vtable slots, counting IUnknown's three methods.
const (
	slotRelease = 2

	// IUIAutomation
	slotElementFromHandleBuildCache = 10
	slotControlViewWalker           = 14
	slotCreateCacheRequest          = 20

	// IUIAutomationCacheRequest
	slotAddProperty = 3

	// IUIAutomationTreeWalker
	slotFirstChildBuildCache  = 10
	slotNextSiblingBuildCache = 12

	// IUIAutomationElement
	slotGetCachedPropertyValue = 12
)

// comCall invokes vtable slot method of the COM object obj.
//
//go:uintptrescapes
func comCall(obj unsafe.Pointer, method int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, method*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

func hresultError(what string, hr uintptr) error {
	if int32(hr) >= 0 {
		return nil
	}
	return fmt.Errorf("%s: %w", what, syscall.Errno(hr))
}

func release(obj unsafe.Pointer) {
	if obj != nil {
		comCall(obj, slotRelease)
	}
}

// UIA is a UI Automation client whose elements carry a fixed set of cached
// properties, so reading an element costs one cross-process call. Use it
// from one OS thread (runtime.LockOSThread).
type UIA struct {
	automation, walker, cache unsafe.Pointer
}

// NewUIA initialises COM on the calling thread and creates the client,
// caching props on every element it returns. The tree is the control view.
func NewUIA(props []int) (*UIA, error) {
	if err := Find(procCoInitializeEx, procCoCreateInstance, procVariantClear); err != nil {
		return nil, err
	}
	// COINIT_MULTITHREADED; S_FALSE (already initialised) is success.
	if err := hresultError("CoInitializeEx", mustCall(procCoInitializeEx, 0, 0)); err != nil {
		return nil, err
	}
	u := &UIA{}
	const clsctxInprocServer = 1
	if err := hresultError("CoCreateInstance(CUIAutomation)", mustCall(procCoCreateInstance,
		uintptr(unsafe.Pointer(&clsidCUIAutomation)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(&u.automation)))); err != nil {
		_, _ = Call(procCoUninitialize)
		return nil, err
	}
	if err := hresultError("ControlViewWalker", comCall(u.automation, slotControlViewWalker, uintptr(unsafe.Pointer(&u.walker)))); err != nil {
		u.Close()
		return nil, err
	}
	if err := hresultError("CreateCacheRequest", comCall(u.automation, slotCreateCacheRequest, uintptr(unsafe.Pointer(&u.cache)))); err != nil {
		u.Close()
		return nil, err
	}
	for _, p := range props {
		if err := hresultError("AddProperty", comCall(u.cache, slotAddProperty, uintptr(p))); err != nil {
			u.Close()
			return nil, err
		}
	}
	return u, nil
}

func mustCall(p *windows.LazyProc, args ...uintptr) uintptr {
	r, _ := Call(p, args...)
	return r
}

// Find loads procs, reporting the first one missing.
func Find(procs ...*windows.LazyProc) error {
	for _, p := range procs {
		if err := p.Find(); err != nil {
			return err
		}
	}
	return nil
}

// Close releases the client and uninitialises COM.
func (u *UIA) Close() {
	release(u.cache)
	release(u.walker)
	release(u.automation)
	u.cache, u.walker, u.automation = nil, nil, nil
	_, _ = Call(procCoUninitialize)
}

// UIAElement is an automation element with cached properties.
type UIAElement struct{ obj unsafe.Pointer }

// Release frees the element.
func (e *UIAElement) Release() {
	if e != nil {
		release(e.obj)
		e.obj = nil
	}
}

// ElementFromHandle returns the element of a window.
func (u *UIA) ElementFromHandle(h HWND) (*UIAElement, error) {
	var out unsafe.Pointer
	if err := hresultError("ElementFromHandle", comCall(u.automation, slotElementFromHandleBuildCache, uintptr(h), uintptr(u.cache), uintptr(unsafe.Pointer(&out)))); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("ElementFromHandle returned no element")
	}
	return &UIAElement{out}, nil
}

// FirstChild returns e's first child in the control view, nil when there
// is none.
func (u *UIA) FirstChild(e *UIAElement) (*UIAElement, error) {
	return u.step(slotFirstChildBuildCache, e)
}

// NextSibling returns e's next sibling in the control view, nil when there
// is none.
func (u *UIA) NextSibling(e *UIAElement) (*UIAElement, error) {
	return u.step(slotNextSiblingBuildCache, e)
}

func (u *UIA) step(slot int, e *UIAElement) (*UIAElement, error) {
	var out unsafe.Pointer
	if err := hresultError("TreeWalker", comCall(u.walker, slot, uintptr(e.obj), uintptr(u.cache), uintptr(unsafe.Pointer(&out)))); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	return &UIAElement{out}, nil
}

// variant is a VARIANT: the type tag, three reserved words, and a union of
// up to two pointers.
type variant struct {
	vt  uint16
	_   [3]uint16
	val [2]uintptr
}

const (
	vtI4    = 3
	vtR8    = 5
	vtBSTR  = 8
	vtBool  = 11
	vtArray = 0x2000
)

// Prop returns the cached value of property id: a string, bool, int32,
// float64 or []float64, or nil when the element does not support it.
func (e *UIAElement) Prop(id int) any {
	var v variant
	if hr := comCall(e.obj, slotGetCachedPropertyValue, uintptr(id), uintptr(unsafe.Pointer(&v))); int32(hr) < 0 {
		return nil
	}
	defer func() { _, _ = Call(procVariantClear, uintptr(unsafe.Pointer(&v))) }()
	union := unsafe.Pointer(&v.val)
	switch v.vt {
	case vtBSTR:
		p := *(*unsafe.Pointer)(union)
		if p == nil {
			return ""
		}
		n, _ := Call(procSysStringLen, uintptr(p))
		return string(utf16.Decode(unsafe.Slice((*uint16)(p), int(n))))
	case vtBool:
		return *(*int16)(union) != 0
	case vtI4:
		return *(*int32)(union)
	case vtR8:
		return *(*float64)(union)
	case vtArray | vtR8:
		psa := *(*unsafe.Pointer)(union)
		var ub int32
		if hr, _ := Call(procSafeArrayGetUBound, uintptr(psa), 1, uintptr(unsafe.Pointer(&ub))); int32(hr) < 0 || ub < 0 {
			return []float64{}
		}
		var data unsafe.Pointer
		if hr, _ := Call(procSafeArrayAccessData, uintptr(psa), uintptr(unsafe.Pointer(&data))); int32(hr) < 0 {
			return []float64{}
		}
		out := append([]float64(nil), unsafe.Slice((*float64)(data), int(ub)+1)...)
		_, _ = Call(procSafeArrayUnaccessData, uintptr(psa))
		return out
	}
	return nil
}
