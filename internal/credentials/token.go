// Package credentials reads rotating bearer tokens without falling back to
// anonymous access when a configured file is unavailable.
package credentials

import (
	"errors"
	"io"
	"os"
	"strings"
)

const MaxTokenBytes = 4096

// ErrUnavailable intentionally omits filesystem paths and secret contents.
var ErrUnavailable = errors.New("authentication credential unavailable")

// Resolve gives the file precedence over a literal token. Only an explicitly
// unconfigured credential (both arguments empty) permits anonymous operation.
// Read on each request so atomic rename can rotate credentials without restart.
func Resolve(token, file string) (string, error) {
	if file != "" {
		info, err := os.Stat(file)
		if err != nil || !info.Mode().IsRegular() || info.Size() > MaxTokenBytes {
			return "", ErrUnavailable
		}
		f, err := os.Open(file)
		if err != nil {
			return "", ErrUnavailable
		}
		defer f.Close()
		info, err = f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return "", ErrUnavailable
		}
		data, err := io.ReadAll(io.LimitReader(f, MaxTokenBytes+1))
		if err != nil || len(data) > MaxTokenBytes {
			return "", ErrUnavailable
		}
		token = strings.TrimSpace(string(data))
		if token == "" {
			return "", ErrUnavailable
		}
	}
	if len(token) > MaxTokenBytes {
		return "", ErrUnavailable
	}
	for _, ch := range []byte(token) {
		if ch <= ' ' || ch >= 127 {
			return "", ErrUnavailable
		}
	}
	return token, nil
}
