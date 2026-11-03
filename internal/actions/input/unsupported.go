package input

import "github.com/CIPFZ/go-agent-platform/internal/protocol"

type unsupportedBackend struct{}

func errUnsupported() error { return protocol.Unsupported("input is not supported on this platform") }

func (unsupportedBackend) Move(int, int) error         { return errUnsupported() }
func (unsupportedBackend) Position() (int, int, error) { return 0, 0, errUnsupported() }
func (unsupportedBackend) Button(Button, bool) error   { return errUnsupported() }
func (unsupportedBackend) Scroll(int, int) error       { return errUnsupported() }
func (unsupportedBackend) Key(Key, bool) error         { return errUnsupported() }
func (unsupportedBackend) TypeUnicode(rune) error      { return errUnsupported() }
func (unsupportedBackend) TypeScancode(rune) error     { return errUnsupported() }
