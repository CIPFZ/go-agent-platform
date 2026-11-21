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

// keyframes asks for a keyframe every two seconds of time: ffmpeg cannot
// produce one on demand, so this bounds how long a viewer waits after
// joining or after loss. An interval counted in frames (-g) would stretch
// whenever capture runs below the nominal rate, which it does: at a nominal
// 60 the interval was three to four seconds. idr is the encoder's option
// that makes a forced keyframe an IDR frame.
func keyframes(idr string) []string {
	return []string{"-g", "100000", idr, "1", "-force_key_frames", "expr:gte(t,n_forced*2)"}
}

// gop is the interval in frames, for encoders whose forced keyframes were
// not tried.
func gop(p Params) string { return strconv.Itoa(p.FPS * 2) }

// toNV12 converts downloaded BGRA frames for the encoder. Measured against a
// plain format=nv12 at a nominal 60: the same frame rate for 1.25 instead of
// 1.8 cores.
const toNV12 = "scale=flags=fast_bilinear:threads=4,format=nv12"

// ddaGraph captures the primary output with Desktop Duplication as the
// source of a filter graph, followed by filters. The pointer is left out:
// the page draws it locally. Frames keep their capture times (passthrough)
// instead of being duplicated to a constant rate.
//
// Capture runs at twice the target rate (at most 60) and select then lets
// through at most the target rate, before the frames are downloaded, which
// is the slow step. Capturing at the target rate itself delivered three
// quarters of it (23 of 30), because capture and download then wait on each
// other; an fps filter in place of select reaches the rate exactly but
// delays every frame by one interval.
func ddaGraph(p Params, filters string) []string {
	source := fmt.Sprintf("ddagrab=output_idx=0:framerate=%d:draw_mouse=0", min(p.FPS*2, 60))
	if p.FPS < 60 {
		// A little less than the frame interval, so that timing jitter
		// does not drop every second frame as well.
		source += fmt.Sprintf(",select='isnan(prev_selected_t)+gte(t-prev_selected_t,%.4f)'", 0.88/float64(p.FPS))
	}
	return []string{"-filter_complex", source + "," + filters + "[v]", "-map", "[v]", "-fps_mode", "passthrough"}
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
	return join([]string{"-c:v", "h264_qsv", "-preset", "veryfast", "-async_depth", "1", "-bf", "0"}, keyframes("-forced_idr"),
		[]string{"-b:v", kbps(p), "-maxrate", strconv.Itoa(p.BitrateKbps*2) + "k", "-bufsize", strconv.Itoa(p.BitrateKbps/3) + "k"})
}

func x264(p Params) []string {
	return join([]string{"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-pix_fmt", "yuv420p"}, keyframes("-forced-idr"),
		[]string{"-b:v", kbps(p), "-maxrate", kbps(p), "-bufsize", strconv.Itoa(p.BitrateKbps/2) + "k"})
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
//     (hwmap, with or without vpp_qsv): 20 ms to 1 s, differently on every
//     click. It uses a quarter of a core instead of about one, but is not
//     usable, so it is not a candidate.
//
// Downloading a frame is what bounds the rate: on that laptop about 33
// frames a second reach the encoder whatever is asked for, although capture
// alone delivers 59.
//
// The NVENC and AMF entries are untested.
func platformCandidates() []candidate {
	return []candidate{
		{name: "dda+h264_qsv", capture: "dda", encoder: "h264_qsv", live: true, needs: []string{"ddagrab", "h264_qsv"},
			args: func(p Params) []string { return join(ddaGraph(p, "hwdownload,format=bgra,"+toNV12), qsv(p)) }},
		{name: "dda+h264_nvenc", capture: "dda", encoder: "h264_nvenc", live: true, needs: []string{"ddagrab", "h264_nvenc"},
			args: func(p Params) []string {
				return join(ddaGraph(p, "hwdownload,format=bgra,"+toNV12), []string{"-c:v", "h264_nvenc", "-preset", "p1", "-tune", "ull",
					"-zerolatency", "1", "-bf", "0", "-g", gop(p), "-rc", "vbr", "-b:v", kbps(p), "-maxrate", strconv.Itoa(p.BitrateKbps*2) + "k"})
			}},
		{name: "dda+h264_amf", capture: "dda", encoder: "h264_amf", live: true, needs: []string{"ddagrab", "h264_amf"},
			args: func(p Params) []string {
				return join(ddaGraph(p, "hwdownload,format=bgra,"+toNV12), []string{"-c:v", "h264_amf", "-usage", "ultralowlatency",
					"-g", gop(p), "-rc", "vbr_peak", "-b:v", kbps(p), "-maxrate", strconv.Itoa(p.BitrateKbps*2) + "k"})
			}},
		{name: "dda+libx264", capture: "dda", encoder: "libx264", live: true, needs: []string{"ddagrab", "libx264"},
			args: func(p Params) []string { return join(ddaGraph(p, "hwdownload,format=bgra"), x264(p)) }},
		{name: "gdi+h264_qsv", capture: "gdi", encoder: "h264_qsv", needs: []string{"gdigrab", "h264_qsv"}, env: highDPI,
			args: func(p Params) []string { return join(gdi(p), []string{"-vf", toNV12}, qsv(p)) }},
		{name: "gdi+libx264", capture: "gdi", encoder: "libx264", needs: []string{"gdigrab", "libx264"}, env: highDPI,
			args: func(p Params) []string { return join(gdi(p), x264(p)) }},
	}
}
