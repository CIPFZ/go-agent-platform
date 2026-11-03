//go:build linux

package clipboard

func newBackend() Backend { return unsupportedBackend{} }
