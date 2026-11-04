package window

import "github.com/CIPFZ/go-agent-platform/internal/protocol"

// unsupportedBackend answers every call with unsupported and a reason.
type unsupportedBackend struct{ reason string }

func (u unsupportedBackend) err() error { return protocol.Unsupported("%s", u.reason) }

func (u unsupportedBackend) List(bool) ([]Window, error)         { return nil, u.err() }
func (u unsupportedBackend) Activate(string) error               { return u.err() }
func (u unsupportedBackend) Close(string) error                  { return u.err() }
func (u unsupportedBackend) SetState(string, State) error        { return u.err() }
func (u unsupportedBackend) SetRect(string, protocol.Rect) error { return u.err() }
