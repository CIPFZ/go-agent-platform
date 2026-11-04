//go:build !linux && !windows

package clipboard

func newBackend() Backend {
	return unsupportedBackend{"clipboard is not supported on this platform"}
}
