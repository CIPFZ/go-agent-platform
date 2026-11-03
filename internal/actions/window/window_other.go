//go:build !linux && !windows

package window

func newBackend() Backend { return unsupportedBackend{} }
