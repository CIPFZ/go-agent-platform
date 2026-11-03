//go:build windows

package clipboard

func newBackend() Backend { return unsupportedBackend{} }
