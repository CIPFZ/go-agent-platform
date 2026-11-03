package api

import (
	"context"
	"errors"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func asProtocolError(err error, target **protocol.Error) bool { return errors.As(err, target) }

func isContextError(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
