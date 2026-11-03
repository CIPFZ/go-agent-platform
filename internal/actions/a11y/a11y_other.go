//go:build !linux && !windows

package a11y

// Supported reports whether an accessibility provider is available.
func Supported() bool { return false }
