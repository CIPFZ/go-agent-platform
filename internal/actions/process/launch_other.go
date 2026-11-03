//go:build !linux && !windows

package process

// Launch is not supported on this platform.
func Launch(Command) (int, error) { return 0, ErrUnsupported }
