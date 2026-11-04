//go:build !linux && !windows

package window

func newBackend() Backend {
	return unsupportedBackend{"window management is not supported on this platform"}
}
