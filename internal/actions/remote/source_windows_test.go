//go:build windows

package remote

import (
	"strings"
	"testing"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// The pipelines were tuned by measurement (docs/remote-desktop-design.md
// 3.5, 3.8); these properties are what the measurements settled.
func TestPipelineArguments(t *testing.T) {
	display := protocol.Rect{W: 2560, H: 1600}
	for _, c := range platformCandidates() {
		for _, fps := range []int{15, 30, 60} {
			args := strings.Join(c.args(Params{FPS: fps, BitrateKbps: 8000, Display: display}), " ")
			// Frames keep their capture time; none are duplicated.
			if !strings.Contains(args, "-fps_mode passthrough") {
				t.Errorf("%s at %d: no passthrough: %s", c.name, fps, args)
			}
			// GPU frames are never handed straight to the encoder.
			if strings.Contains(args, "hwmap") {
				t.Errorf("%s at %d: uses hwmap: %s", c.name, fps, args)
			}
			if c.capture != "dda" {
				continue
			}
			// The capture is the graph's source, not an input device, and
			// runs at twice the target rate, at most 60.
			if strings.Contains(args, "-f lavfi") || !strings.Contains(args, "-filter_complex ddagrab=") {
				t.Errorf("%s at %d: capture is not the graph source: %s", c.name, fps, args)
			}
			want := map[int]string{15: "framerate=30:", 30: "framerate=60:", 60: "framerate=60:"}[fps]
			if !strings.Contains(args, want) {
				t.Errorf("%s at %d: want %s in %s", c.name, fps, want, args)
			}
			// Below 60 frames are dropped before the download.
			sel, dl := strings.Index(args, "select="), strings.Index(args, "hwdownload")
			if (fps < 60) != (sel >= 0) || (sel >= 0 && sel > dl) {
				t.Errorf("%s at %d: select at %d, hwdownload at %d: %s", c.name, fps, sel, dl, args)
			}
		}
	}
	// The two encoders that were measured force a keyframe by time.
	for _, name := range []string{"dda+h264_qsv", "dda+libx264", "gdi+h264_qsv", "gdi+libx264"} {
		for _, c := range platformCandidates() {
			if c.name != name {
				continue
			}
			args := strings.Join(c.args(Params{FPS: 30, BitrateKbps: 8000, Display: display}), " ")
			if !strings.Contains(args, "-force_key_frames expr:gte(t,n_forced*2)") || !strings.Contains(args, "orced") {
				t.Errorf("%s: no timed keyframes: %s", name, args)
			}
		}
	}
}
