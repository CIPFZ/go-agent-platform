//go:build windows

package remote

import (
	"errors"

	"github.com/CIPFZ/go-agent-platform/internal/platform/win"
)

const audioCaptureSupported = true

// captureAudio records what the default playback device plays (WASAPI
// loopback) until stop is closed.
func captureAudio(stop <-chan struct{}, ready func(pcmFormat), pcm func([]byte)) error {
	err := win.Loopback(stop, func(f win.LoopbackFormat) { ready(pcmFormat{SampleRate: f.SampleRate, Channels: f.Channels}) }, pcm)
	if errors.Is(err, win.ErrLoopbackDeviceChanged) {
		return errAudioRestart
	}
	return err
}
