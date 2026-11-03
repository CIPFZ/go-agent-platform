package async

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunReturnsResult(t *testing.T) {
	v, err := Run(context.Background(), func() (int, error) { return 7, nil })
	if v != 7 || err != nil {
		t.Fatalf("got %d, %v", v, err)
	}
}

func TestRunAbandonsOnDeadline(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, func() (int, error) {
		<-release
		close(finished)
		return 1, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("abandoned call did not finish")
	}
}
