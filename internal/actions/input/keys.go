package input

import (
	"sort"
	"strings"
)

// The key table of docs/keys.md. Windows uses VK plus the set-1 scan code
// and extended-key flag; Linux uses the X keysym. keys_doc_test.go checks
// that docs/keys.md lists exactly these keys and aliases.
var keyTable = buildKeys()

// keyAliases maps alternative names to canonical names.
var keyAliases = map[string]string{
	"control": "ctrl", "lctrl": "ctrl", "lcontrol": "ctrl",
	"lalt": "alt", "option": "alt", "altgr": "ralt",
	"lshift": "shift",
	"super":  "win", "lwin": "win", "meta": "win", "cmd": "win", "rsuper": "rwin",
	"return": "enter", "esc": "escape", "del": "delete", "ins": "insert",
	"pgup": "pageup", "pgdn": "pagedown", "back": "backspace",
	"apps": "menu", "prtsc": "printscreen", "print": "printscreen", "break": "pause",
	"caps": "capslock", "spacebar": "space",
	"-": "minus", "=": "equal", "[": "bracketleft", "]": "bracketright", "\\": "backslash",
	";": "semicolon", "'": "quote", "`": "backquote", "grave": "backquote", ",": "comma",
	".": "period", "/": "slash",
	"num_enter": "numpad_enter", "kp_enter": "numpad_enter",
	"mute": "volume_mute", "play_pause": "media_play_pause",
}

func buildKeys() map[string]Key {
	t := map[string]Key{}
	add := func(name string, vk, scan uint16, ext bool, keysym uint32) {
		t[name] = Key{Name: name, VK: vk, Scan: scan, Ext: ext, Keysym: keysym}
	}
	letterScan := map[byte]uint16{
		'a': 0x1E, 'b': 0x30, 'c': 0x2E, 'd': 0x20, 'e': 0x12, 'f': 0x21, 'g': 0x22, 'h': 0x23, 'i': 0x17,
		'j': 0x24, 'k': 0x25, 'l': 0x26, 'm': 0x32, 'n': 0x31, 'o': 0x18, 'p': 0x19, 'q': 0x10, 'r': 0x13,
		's': 0x1F, 't': 0x14, 'u': 0x16, 'v': 0x2F, 'w': 0x11, 'x': 0x2D, 'y': 0x15, 'z': 0x2C,
	}
	for c := byte('a'); c <= 'z'; c++ {
		add(string(c), uint16(c-'a'+'A'), letterScan[c], false, uint32(c))
	}
	digitScan := []uint16{0x0B, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A}
	for d := 0; d <= 9; d++ {
		add(string(rune('0'+d)), uint16('0'+d), digitScan[d], false, uint32('0'+d))
	}
	fScan := []uint16{0x3B, 0x3C, 0x3D, 0x3E, 0x3F, 0x40, 0x41, 0x42, 0x43, 0x44, 0x57, 0x58,
		0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6A, 0x6B, 0x6C, 0x6D, 0x6E, 0x76}
	for i := 0; i < 24; i++ {
		add("f"+itoa(i+1), uint16(0x70+i), fScan[i], false, uint32(0xffbe+i))
	}
	// Modifiers: the left-hand keys are canonical.
	add("ctrl", 0xA2, 0x1D, false, 0xffe3)
	add("rctrl", 0xA3, 0x1D, true, 0xffe4)
	add("alt", 0xA4, 0x38, false, 0xffe9)
	add("ralt", 0xA5, 0x38, true, 0xffea)
	add("shift", 0xA0, 0x2A, false, 0xffe1)
	add("rshift", 0xA1, 0x36, false, 0xffe2)
	add("win", 0x5B, 0x5B, true, 0xffeb)
	add("rwin", 0x5C, 0x5C, true, 0xffec)
	// Navigation.
	add("up", 0x26, 0x48, true, 0xff52)
	add("down", 0x28, 0x50, true, 0xff54)
	add("left", 0x25, 0x4B, true, 0xff51)
	add("right", 0x27, 0x4D, true, 0xff53)
	add("home", 0x24, 0x47, true, 0xff50)
	add("end", 0x23, 0x4F, true, 0xff57)
	add("pageup", 0x21, 0x49, true, 0xff55)
	add("pagedown", 0x22, 0x51, true, 0xff56)
	// Editing and system.
	add("enter", 0x0D, 0x1C, false, 0xff0d)
	add("tab", 0x09, 0x0F, false, 0xff09)
	add("space", 0x20, 0x39, false, 0x0020)
	add("backspace", 0x08, 0x0E, false, 0xff08)
	add("delete", 0x2E, 0x53, true, 0xffff)
	add("insert", 0x2D, 0x52, true, 0xff63)
	add("escape", 0x1B, 0x01, false, 0xff1b)
	add("capslock", 0x14, 0x3A, false, 0xffe5)
	add("numlock", 0x90, 0x45, true, 0xff7f)
	add("scrolllock", 0x91, 0x46, false, 0xff14)
	add("printscreen", 0x2C, 0x37, true, 0xff61)
	add("pause", 0x13, 0x45, false, 0xff13)
	add("menu", 0x5D, 0x5D, true, 0xff67)
	// Punctuation (US layout positions).
	add("minus", 0xBD, 0x0C, false, 0x002d)
	add("equal", 0xBB, 0x0D, false, 0x003d)
	add("bracketleft", 0xDB, 0x1A, false, 0x005b)
	add("bracketright", 0xDD, 0x1B, false, 0x005d)
	add("backslash", 0xDC, 0x2B, false, 0x005c)
	add("semicolon", 0xBA, 0x27, false, 0x003b)
	add("quote", 0xDE, 0x28, false, 0x0027)
	add("backquote", 0xC0, 0x29, false, 0x0060)
	add("comma", 0xBC, 0x33, false, 0x002c)
	add("period", 0xBE, 0x34, false, 0x002e)
	add("slash", 0xBF, 0x35, false, 0x002f)
	// Numeric keypad.
	numScan := []uint16{0x52, 0x4F, 0x50, 0x51, 0x4B, 0x4C, 0x4D, 0x47, 0x48, 0x49}
	for d := 0; d <= 9; d++ {
		add("numpad"+itoa(d), uint16(0x60+d), numScan[d], false, uint32(0xffb0+d))
	}
	add("numpad_multiply", 0x6A, 0x37, false, 0xffaa)
	add("numpad_add", 0x6B, 0x4E, false, 0xffab)
	add("numpad_subtract", 0x6D, 0x4A, false, 0xffad)
	add("numpad_decimal", 0x6E, 0x53, false, 0xffae)
	add("numpad_divide", 0x6F, 0x35, true, 0xffaf)
	add("numpad_enter", 0x0D, 0x1C, true, 0xff8d)
	// Media keys (X keysyms from XF86keysym.h).
	add("volume_mute", 0xAD, 0x20, true, 0x1008ff12)
	add("volume_down", 0xAE, 0x2E, true, 0x1008ff11)
	add("volume_up", 0xAF, 0x30, true, 0x1008ff13)
	add("media_next", 0xB0, 0x19, true, 0x1008ff17)
	add("media_prev", 0xB1, 0x10, true, 0x1008ff16)
	add("media_stop", 0xB2, 0x24, true, 0x1008ff15)
	add("media_play_pause", 0xB3, 0x22, true, 0x1008ff14)
	return t
}

// LookupKey resolves a key name case-insensitively, including aliases.
func LookupKey(name string) (Key, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if len(name) == 1 {
		n = name // single characters such as "-" are matched as given
		if c := name[0]; c >= 'A' && c <= 'Z' {
			n = strings.ToLower(name)
		}
	}
	if canonical, ok := keyAliases[n]; ok {
		n = canonical
	}
	k, ok := keyTable[n]
	return k, ok
}

// KeyNames returns the canonical key names, sorted.
func KeyNames() []string {
	out := make([]string, 0, len(keyTable))
	for name := range keyTable {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Aliases returns the alias table.
func Aliases() map[string]string {
	out := make(map[string]string, len(keyAliases))
	for k, v := range keyAliases {
		out[k] = v
	}
	return out
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
