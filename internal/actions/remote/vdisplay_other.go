//go:build !windows

package remote

// PlatformVirtualDisplay is nil where no virtual display driver is used.
func PlatformVirtualDisplay() VirtualDisplay { return nil }
