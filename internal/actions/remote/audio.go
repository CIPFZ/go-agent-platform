package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/pion/rtp"

	"github.com/CIPFZ/go-agent-platform/internal/actions/process"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// AudioSource produces the desktop's sound as an Opus RTP stream. Run blocks
// until ctx is done or sound cannot be captured.
type AudioSource interface {
	Available() error
	Run(ctx context.Context, emit func(*rtp.Packet)) error
}

// pcmFormat describes captured audio: signed 16-bit little-endian samples.
type pcmFormat struct {
	SampleRate int
	Channels   int
}

// errAudioRestart asks for the capture to be started again, for example
// after the playback device changed.
var errAudioRestart = errors.New("the audio capture must restart")

// ffmpegAudio captures the system's playback and has ffmpeg encode it.
type ffmpegAudio struct{ ffmpeg string }

func newFFmpegAudio(ffmpeg string) *ffmpegAudio { return &ffmpegAudio{ffmpeg: ffmpeg} }

func (a *ffmpegAudio) Available() error {
	if !audioCaptureSupported {
		return protocol.Unsupported("sound capture is not supported on this platform")
	}
	if a.ffmpeg == "" {
		return protocol.Unsupported("sound needs ffmpeg, which was not found")
	}
	return nil
}

func (a *ffmpegAudio) Run(ctx context.Context, emit func(*rtp.Packet)) error {
	if err := a.Available(); err != nil {
		return err
	}
	failures := 0
	for ctx.Err() == nil {
		started := time.Now()
		err := a.runOnce(ctx, emit)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) > 30*time.Second {
			failures = 0
		}
		if errors.Is(err, errAudioRestart) {
			logf("audio: the playback device changed; restarting the capture")
		} else if failures++; failures >= 3 {
			return err
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return nil
}

func (a *ffmpegAudio) runOnce(ctx context.Context, emit func(*rtp.Packet)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, rtcpConn, err := listenRTP()
	if err != nil {
		return err
	}
	defer conn.Close()
	defer rtcpConn.Close()

	// The capture decides the format, so the encoder starts once it is known.
	formats := make(chan pcmFormat, 1)
	pr, pw := io.Pipe()
	captured := make(chan error, 1)
	go func() {
		err := captureAudio(ctx.Done(), func(f pcmFormat) { formats <- f }, func(b []byte) { _, _ = pw.Write(b) })
		_ = pw.CloseWithError(io.EOF)
		captured <- err
	}()
	var format pcmFormat
	select {
	case format = <-formats:
	case err := <-captured:
		if err == nil {
			err = errors.New("the audio capture ended")
		}
		return err
	case <-time.After(5 * time.Second):
		return errors.New("the audio capture did not start")
	}

	logf("audio: capturing %d Hz, %d channels", format.SampleRate, format.Channels)
	cmd := exec.Command(a.ffmpeg, "-hide_banner", "-loglevel", "error", "-fflags", "nobuffer", "-probesize", "32", "-analyzeduration", "0",
		"-f", "s16le", "-ar", strconv.Itoa(format.SampleRate), "-ac", strconv.Itoa(format.Channels), "-i", "pipe:0",
		// 10 ms frames keep the delay low; WebRTC carries Opus as 48 kHz stereo.
		"-c:a", "libopus", "-application", "lowdelay", "-frame_duration", "10", "-b:a", "96k", "-ar", "48000", "-ac", "2",
		"-f", "rtp", "rtp://127.0.0.1:"+strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port))
	cmd.Env = os.Environ()
	cmd.Stdin = pr
	stderr := &tail{max: 4 << 10}
	cmd.Stderr = stderr
	tree, err := process.StartTree(cmd)
	if err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- tree.Wait() }()
	defer func() {
		cancel()
		_ = pr.CloseWithError(io.ErrClosedPipe)
		_ = tree.Kill()
		<-exited
	}()

	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			return nil
		case werr := <-exited:
			exited <- werr
			return fmt.Errorf("ffmpeg exited: %s", firstLine(stderr.String(), werr))
		case cerr := <-captured:
			if errors.Is(cerr, errAudioRestart) {
				return cerr
			}
			return fmt.Errorf("audio capture: %w", cerr)
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, rerr := conn.Read(buf)
		if rerr != nil {
			var nerr net.Error
			if errors.As(rerr, &nerr) && nerr.Timeout() {
				continue
			}
			return rerr
		}
		pkt := &rtp.Packet{}
		if n > 12 && pkt.Unmarshal(append([]byte(nil), buf[:n]...)) == nil {
			emit(pkt)
		}
	}
}
