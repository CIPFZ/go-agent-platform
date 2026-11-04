//go:build linux

package record

import (
	"fmt"
	"os"

	"github.com/CIPFZ/go-agent-platform/internal/platform/x11"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

const grabberName = "x11grab"

// grabArgs captures r with x11grab, translating API coordinates to the root
// window (spec section 10). libx264 with yuv420p needs even dimensions.
func grabArgs(fps int, r protocol.Rect) ([]string, error) {
	c, err := x11.Shared()
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeNoDisplay, "%v", err)
	}
	ox, oy, err := c.Origin()
	if err != nil {
		return nil, err
	}
	display := os.Getenv("DISPLAY")
	return []string{"-f", "x11grab", "-framerate", fmt.Sprint(fps), "-video_size", fmt.Sprintf("%dx%d", r.W&^1, r.H&^1),
		"-i", fmt.Sprintf("%s+%d,%d", display, r.X+ox, r.Y+oy)}, nil
}

func ffmpegEnv(env []string) []string { return env }
