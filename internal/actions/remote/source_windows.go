//go:build windows

package remote

import (
	"fmt"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/CIPFZ/go-agent-platform/internal/platform/win"
)

func platformDisplayActive() bool { return win.DisplayActive() }

// hideWindow keeps a console child from opening a window on the desktop.
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

func kbps(p Params) string { return strconv.Itoa(p.BitrateKbps) + "k" }

// gop is a keyframe every two seconds: ffmpeg cannot produce one on demand,
// so this bounds how long a viewer waits after joining or after loss.
func gop(p Params) string { return strconv.Itoa(p.FPS * 2) }

// ddaGraph captures the primary output with Desktop Duplication as the
// source of a filter graph, followed by filters. The pointer is left out:
// the page draws it locally. Frames keep their capture times (passthrough)
// instead of being duplicated to a constant rate.
func ddaGraph(p Params, filters string) []string {
	return []string{"-filter_complex", fmt.Sprintf("ddagrab=output_idx=0:framerate=%d:draw_mouse=0,%s[v]", p.FPS, filters),
		"-map", "[v]", "-fps_mode", "passthrough"}
}

// gdi captures with BitBlt. It is the only capture that returns frames while
// no display is active (a closed laptop lid), at about 16 frames a second.
func gdi(p Params) []string {
	return []string{"-f", "gdigrab", "-framerate", strconv.Itoa(p.FPS), "-draw_mouse", "0",
		"-offset_x", strconv.Itoa(p.Display.X), "-offset_y", strconv.Itoa(p.Display.Y),
		"-video_size", fmt.Sprintf("%dx%d", p.Display.W&^1, p.Display.H&^1), "-i", "desktop", "-fps_mode", "passthrough"}
}

// qsv encodes with a variable bitrate: a still desktop costs almost nothing
// and a change may briefly use twice the target. A constant bitrate pads
// every frame to the same size.
func qsv(p Params) []string {
	return []string{"-c:v", "h264_qsv", "-preset", "veryfast", "-async_depth", "1", "-bf", "0", "-g", gop(p),
		"-b:v", kbps(p), "-maxrate", strconv.Itoa(p.BitrateKbps*2) + "k", "-bufsize", strconv.Itoa(p.BitrateKbps/3) + "k"}
}

func x264(p Params) []string {
	return []string{"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-pix_fmt", "yuv420p", "-g", gop(p),
		"-b:v", kbps(p), "-maxrate", kbps(p), "-bufsize", strconv.Itoa(p.BitrateKbps/2) + "k"}
}

func join(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// highDPI makes ffmpeg DPI aware so gdigrab works in physical pixels at
// display scales above 100 % (as for recording, spec 10).
func highDPI(env []string) []string { return append(env, "__COMPAT_LAYER=HighDpiAware") }

// platformCandidates lists the pipelines in order of preference. Measured on
// an Iris Xe laptop at 2560x1600 (docs/remote-desktop-design.md 3.4), from a
// click to the changed frame arriving over the LAN:
//
//   - Desktop Duplication, frames downloaded to memory, h264_qsv: 41 to 83 ms.
//   - Desktop Duplication, frames downloaded, libx264: 3 to 92 ms.
//   - GDI, h264_qsv: 59 to 113 ms.
//   - Desktop Duplication with the GPU frames handed straight to h264_qsv
//     (hwmap): 20 ms to 1 s, differently on every click. It uses a quarter of
//     a core instead of about one and a half, but is not usable, so it is not
//     a candidate.
//
// The NVENC and AMF entries are untested.
func platformCandidates() []candidate {
	return []candidate{
		{name: "dda+h264_qsv", capture: "dda", encoder: "h264_qsv", live: true, needs: []string{"ddagrab", "h264_qsv"},
			args: func(p Params) []string { return join(ddaGraph(p, "hwdownload,format=bgra,format=nv12"), qsv(p)) }},
		{name: "dda+h264_nvenc", capture: "dda", encoder: "h264_nvenc", live: true, needs: []string{"ddagrab", "h264_nvenc"},
			args: func(p Params) []string {
				return join(ddaGraph(p, "hwdownload,format=bgra,format=nv12"), []string{"-c:v", "h264_nvenc", "-preset", "p1", "-tune", "ull",
					"-zerolatency", "1", "-bf", "0", "-g", gop(p), "-rc", "vbr", "-b:v", kbps(p), "-maxrate", strconv.Itoa(p.BitrateKbps*2) + "k"})
			}},
		{name: "dda+h264_amf", capture: "dda", encoder: "h264_amf", live: true, needs: []string{"ddagrab", "h264_amf"},
			args: func(p Params) []string {
				return join(ddaGraph(p, "hwdownload,format=bgra,format=nv12"), []string{"-c:v", "h264_amf", "-usage", "ultralowlatency",
					"-g", gop(p), "-rc", "vbr_peak", "-b:v", kbps(p), "-maxrate", strconv.Itoa(p.BitrateKbps*2) + "k"})
			}},
		{name: "dda+libx264", capture: "dda", encoder: "libx264", live: true, needs: []string{"ddagrab", "libx264"},
			args: func(p Params) []string { return join(ddaGraph(p, "hwdownload,format=bgra"), x264(p)) }},
		{name: "gdi+h264_qsv", capture: "gdi", encoder: "h264_qsv", needs: []string{"gdigrab", "h264_qsv"}, env: highDPI,
			args: func(p Params) []string { return join(gdi(p), []string{"-vf", "format=nv12"}, qsv(p)) }},
		{name: "gdi+libx264", capture: "gdi", encoder: "libx264", needs: []string{"gdigrab", "libx264"}, env: highDPI,
			args: func(p Params) []string { return join(gdi(p), x264(p)) }},
	}
}
