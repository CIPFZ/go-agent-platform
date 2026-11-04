//go:build !linux

package api

import "context"

// platformOrigin is (0, 0): Windows screen coordinates already have the
// primary monitor's corner at the origin.
func platformOrigin(context.Context) (int, int, error) { return 0, 0, nil }
