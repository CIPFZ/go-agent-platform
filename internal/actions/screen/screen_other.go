//go:build !linux && !windows

package screen

func newBackend() Backend { return unsupportedBackend{} }
