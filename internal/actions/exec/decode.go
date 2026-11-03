package exec

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// DecodeOutput converts command output to a string: UTF-8 with invalid bytes
// replaced, except that output of cmd/cmd.exe which is not valid UTF-8 is
// first decoded from the current console code page (spec section 6.5).
func DecodeOutput(argv0 string, b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	base := strings.ToLower(filepath.Base(argv0))
	if base == "cmd" || base == "cmd.exe" {
		if s, ok := decodeCodePage(b); ok {
			return s
		}
	}
	return strings.ToValidUTF8(string(b), "�")
}
