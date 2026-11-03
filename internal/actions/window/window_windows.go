//go:build windows

package window

func newBackend() Backend { return unsupportedBackend{} }
