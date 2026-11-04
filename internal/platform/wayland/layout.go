//go:build linux

package wayland

// usLayout maps the printable ASCII characters to evdev key codes and the
// Shift state on a US keyboard layout, for typing through uinput. The
// compositor applies its own layout, so the VM image should use "us" for
// text to arrive unchanged; characters outside this table need the portal
// path, which sends keysyms.
var usLayout = func() map[rune]struct {
	Code  uint16
	Shift bool
} {
	m := map[rune]struct {
		Code  uint16
		Shift bool
	}{}
	add := func(chars string, codes []uint16, shift bool) {
		for i, r := range chars {
			m[r] = struct {
				Code  uint16
				Shift bool
			}{codes[i], shift}
		}
	}
	letters := []uint16{30, 48, 46, 32, 18, 33, 34, 35, 23, 36, 37, 38, 50, 49, 24, 25, 16, 19, 31, 20, 22, 47, 17, 45, 21, 44}
	add("abcdefghijklmnopqrstuvwxyz", letters, false)
	add("ABCDEFGHIJKLMNOPQRSTUVWXYZ", letters, true)
	digits := []uint16{11, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	add("0123456789", digits, false)
	add(")!@#$%^&*(", digits, true)
	punct := []uint16{12, 13, 26, 27, 43, 39, 40, 41, 51, 52, 53, 57}
	add("-=[]\\;'`,./ ", punct, false)
	add("_+{}|:\"~<>?", punct[:11], true)
	return m
}()

// LayoutKey returns the evdev code and Shift state that type r on a US
// layout.
func LayoutKey(r rune) (code uint16, shift bool, ok bool) {
	k, ok := usLayout[r]
	return k.Code, k.Shift, ok
}

// KeyShift is evdev KEY_LEFTSHIFT.
const KeyShift = 42
