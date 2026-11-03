//go:build linux

package input

func newBackend() Backend { return unsupportedBackend{} }
