package clipboard

import (
	"context"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// unsupportedBackend answers every call with unsupported and a reason.
type unsupportedBackend struct{ reason string }

func (u unsupportedBackend) err() error { return protocol.Unsupported("%s", u.reason) }

func (u unsupportedBackend) Read(context.Context) (Content, error)   { return Content{}, u.err() }
func (u unsupportedBackend) WriteText(context.Context, string) error { return u.err() }
func (u unsupportedBackend) WritePNG(context.Context, []byte) error  { return u.err() }
