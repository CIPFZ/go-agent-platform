//go:build windows

package input

func newBackend() Backend { return unsupportedBackend{} }
