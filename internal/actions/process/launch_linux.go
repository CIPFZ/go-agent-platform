//go:build linux

package process

// Launch starts argv detached from the service and returns its pid.
func Launch(argv []string, dir string, env []string) (int, error) { return 0, ErrUnsupported }
