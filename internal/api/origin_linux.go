//go:build linux

package api

import (
	"context"

	"github.com/CIPFZ/go-agent-platform/internal/async"
	"github.com/CIPFZ/go-agent-platform/internal/platform/x11"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// platformOrigin returns the root-window position of the primary monitor.
func platformOrigin(ctx context.Context) (int, int, error) {
	type xy struct{ x, y int }
	p, err := async.Run(ctx, func() (xy, error) {
		c, err := x11.Shared()
		if err != nil {
			return xy{}, protocol.Errorf(protocol.CodeNoDisplay, "%v", err)
		}
		x, y, err := c.Origin()
		return xy{x, y}, err
	})
	return p.x, p.y, err
}
