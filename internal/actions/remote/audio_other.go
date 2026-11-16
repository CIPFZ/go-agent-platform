//go:build !windows

package remote

import "errors"

const audioCaptureSupported = false

func captureAudio(<-chan struct{}, func(pcmFormat), func([]byte)) error {
	return errors.New("sound capture is not supported on this platform")
}
