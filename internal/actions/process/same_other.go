//go:build !linux && !windows

package process

// IsSameProgram is not implemented on this platform.
func IsSameProgram(int) bool { return false }
