//go:build linux

package file

// KnownDirs returns the user's special directories keyed by name.
func KnownDirs() (map[string]string, error) { return nil, errUnsupported }
