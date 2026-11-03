//go:build windows

package screen

func newBackend() Backend { return unsupportedBackend{} }
