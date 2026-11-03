//go:build !linux && !windows

package clipboard

func newBackend() Backend { return unsupportedBackend{} }
