//go:build linux

package window

func newBackend() Backend { return unsupportedBackend{} }
