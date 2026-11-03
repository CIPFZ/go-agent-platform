//go:build linux

package screen

func newBackend() Backend { return unsupportedBackend{} }
