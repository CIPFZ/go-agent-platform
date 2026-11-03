//go:build windows

package win

import (
	"testing"
	"unsafe"
)

// SendInput rejects calls whose cbSize is not sizeof(INPUT) (40 on amd64).
func TestInputStructSizes(t *testing.T) {
	if s := unsafe.Sizeof(mouseInput{}); s != 40 {
		t.Fatalf("mouseInput is %d bytes", s)
	}
	if s := unsafe.Sizeof(keyboardInput{}); s != 40 {
		t.Fatalf("keyboardInput is %d bytes", s)
	}
	if o := unsafe.Offsetof(mouseInput{}.dx); o != 8 {
		t.Fatalf("union starts at %d", o)
	}
	if o := unsafe.Offsetof(keyboardInput{}.extra); o != 24 {
		t.Fatalf("dwExtraInfo at %d", o)
	}
}
