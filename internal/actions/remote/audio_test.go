package remote

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type audioRunFunc func(context.Context, func(*rtp.Packet)) error

func (audioRunFunc) Available() error { return nil }

func (run audioRunFunc) Run(ctx context.Context, emit func(*rtp.Packet)) error {
	return run(ctx, emit)
}

func TestSessionAudioAfterLockedCaptureFails(t *testing.T) {
	for _, tc := range []struct {
		name              string
		unlockBeforeError bool
		disconnectLocked  bool
	}{
		{name: "unlock after failure"},
		{name: "unlock during failure", unlockBeforeError: true},
		{name: "disconnect while locked", disconnectLocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var locked atomic.Bool
				locked.Store(true)
				var attempts atomic.Int32
				fail := make(chan struct{})
				source := audioRunFunc(func(ctx context.Context, emit func(*rtp.Packet)) error {
					if attempts.Add(1) == 1 {
						<-fail
						return errors.New("playback device unavailable")
					}
					emit(&rtp.Packet{Header: rtp.Header{Version: 2}})
					<-ctx.Done()
					return nil
				})
				track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "test")
				if err != nil {
					t.Fatal(err)
				}
				s := &session{ctx: ctx, audioTrack: track, m: &Manager{aud: source,
					o: Options{Desktop: func() Desktop { return Desktop{Locked: locked.Load()} }}}}
				done := make(chan struct{})
				go func() { defer close(done); s.audio() }()
				synctest.Wait()
				if tc.unlockBeforeError {
					locked.Store(false)
				}
				close(fail)
				synctest.Wait()
				if locked.Load() {
					time.Sleep(5 * time.Second)
					synctest.Wait()
					if attempts.Load() != 1 || s.audioOn.Load() {
						t.Fatalf("still locked: attempts=%d audio=%v", attempts.Load(), s.audioOn.Load())
					}
				}
				if !tc.disconnectLocked {
					locked.Store(false)
					time.Sleep(2 * time.Second)
					synctest.Wait()
					if attempts.Load() != 2 || !s.audioOn.Load() {
						t.Fatalf("after unlock: attempts=%d audio=%v", attempts.Load(), s.audioOn.Load())
					}
				}
				cancel()
				<-done
				if s.audioOn.Load() {
					t.Fatal("audio remained active after disconnect")
				}
			})
		})
	}
}
