package remote

import (
	"strconv"

	"github.com/CIPFZ/go-agent-platform/internal/actions/input"
)

// codeKeys maps a browser KeyboardEvent.code, which names a physical key
// position, to a key of docs/keys.md. Sending positions rather than
// characters lets the remote keyboard layout and input method decide what a
// key produces, as if it were pressed on the remote keyboard.
var codeKeys = buildCodeKeys()

func buildCodeKeys() map[string]string {
	m := map[string]string{
		"ControlLeft": "ctrl", "ControlRight": "rctrl", "AltLeft": "alt", "AltRight": "ralt",
		"ShiftLeft": "shift", "ShiftRight": "rshift", "MetaLeft": "win", "MetaRight": "rwin",
		"ArrowUp": "up", "ArrowDown": "down", "ArrowLeft": "left", "ArrowRight": "right",
		"Home": "home", "End": "end", "PageUp": "pageup", "PageDown": "pagedown",
		"Enter": "enter", "Tab": "tab", "Space": "space", "Backspace": "backspace", "Delete": "delete",
		"Insert": "insert", "Escape": "escape", "CapsLock": "capslock", "NumLock": "numlock",
		"ScrollLock": "scrolllock", "PrintScreen": "printscreen", "Pause": "pause", "ContextMenu": "menu",
		"Minus": "minus", "Equal": "equal", "BracketLeft": "bracketleft", "BracketRight": "bracketright",
		"Backslash": "backslash", "IntlBackslash": "backslash", "Semicolon": "semicolon", "Quote": "quote",
		"Backquote": "backquote", "Comma": "comma", "Period": "period", "Slash": "slash",
		"NumpadMultiply": "numpad_multiply", "NumpadAdd": "numpad_add", "NumpadSubtract": "numpad_subtract",
		"NumpadDecimal": "numpad_decimal", "NumpadDivide": "numpad_divide", "NumpadEnter": "numpad_enter",
		"AudioVolumeMute": "volume_mute", "AudioVolumeDown": "volume_down", "AudioVolumeUp": "volume_up",
		"MediaTrackNext": "media_next", "MediaTrackPrevious": "media_prev", "MediaStop": "media_stop",
		"MediaPlayPause": "media_play_pause",
	}
	for c := 'A'; c <= 'Z'; c++ {
		m["Key"+string(c)] = string(c + 'a' - 'A')
	}
	for d := 0; d <= 9; d++ {
		m["Digit"+strconv.Itoa(d)] = strconv.Itoa(d)
		m["Numpad"+strconv.Itoa(d)] = "numpad" + strconv.Itoa(d)
	}
	for i := 1; i <= 24; i++ {
		m["F"+strconv.Itoa(i)] = "f" + strconv.Itoa(i)
	}
	return m
}

// keyForCode resolves a KeyboardEvent.code.
func keyForCode(code string) (input.Key, bool) {
	name, ok := codeKeys[code]
	if !ok {
		return input.Key{}, false
	}
	return input.LookupKey(name)
}
