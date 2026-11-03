// Package async runs calls that may block indefinitely (screen capture,
// window enumeration, clipboard access) under a context, as required by spec
// section 7: when the context ends the caller gets ctx.Err() at once and the
// call is abandoned on its goroutine, which must still have a natural exit
// path (a system call returning, a child process being killed).
package async

import "context"

// Run runs fn on a new goroutine and waits for it or for ctx, whichever
// comes first.
func Run[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type outcome struct {
		value T
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		v, err := fn()
		done <- outcome{v, err}
	}()
	select {
	case o := <-done:
		return o.value, o.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}
