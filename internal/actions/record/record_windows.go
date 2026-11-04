//go:build windows

package record

import (
	"fmt"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

const grabberName = "gdigrab"

// grabArgs captures r with gdigrab; desktop coordinates equal API
// coordinates because the primary monitor's corner is (0, 0) (spec 10).
func grabArgs(fps int, r protocol.Rect) ([]string, error) {
	return []string{"-f", "gdigrab", "-framerate", fmt.Sprint(fps), "-offset_x", fmt.Sprint(r.X), "-offset_y", fmt.Sprint(r.Y),
		"-video_size", fmt.Sprintf("%dx%d", r.W&^1, r.H&^1), "-i", "desktop"}, nil
}

// ffmpegEnv makes ffmpeg DPI aware through the HighDpiAware compatibility
// layer; otherwise gdigrab works in scaled coordinates at display scales
// above 100 % and captures the wrong area.
func ffmpegEnv(env []string) []string { return append(env, "__COMPAT_LAYER=HighDpiAware") }
