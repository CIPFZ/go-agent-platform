//go:build !linux && !windows

package process

func list() ([]Info, error) { return nil, ErrUnsupported }
func exists(int) bool       { return false }
func terminate(int) error   { return ErrUnsupported }
func forceKill(int) error   { return ErrUnsupported }
