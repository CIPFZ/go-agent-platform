//go:build !linux && !windows

package process

type treeSys struct{}

func (t *Tree) start() error { return ErrUnsupported }

func (t *Tree) kill() error { return ErrUnsupported }

func (t *Tree) release() {}
