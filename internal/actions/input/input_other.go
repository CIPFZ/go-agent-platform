//go:build !linux && !windows

package input

func newBackend() Backend { return unsupportedBackend{} }
