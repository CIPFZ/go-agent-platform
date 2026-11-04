// Package clipboard implements /v1/clipboard for text and PNG images.
package clipboard

import (
	"bytes"
	"context"
	"image/png"

	"github.com/CIPFZ/go-agent-platform/internal/async"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Content is what the clipboard currently offers. Formats lists the
// platform format names present (Windows clipboard format names, X
// selection targets); Text and PNG are filled when a text or image format
// is among them.
type Content struct {
	Formats []string
	Text    *string
	PNG     []byte
}

// Backend reads and writes the system clipboard. Implementations must
// return once ctx ends.
type Backend interface {
	Read(ctx context.Context) (Content, error)
	WriteText(ctx context.Context, text string) error
	WritePNG(ctx context.Context, png []byte) error
}

// NewBackend returns the backend for the current platform and session.
func NewBackend() Backend { return newBackend() }

// MaxBytes bounds the data read from another clipboard owner.
const MaxBytes = 64 << 20

// Service validates requests and runs backend calls under the request
// context on an abandonable goroutine (spec section 7).
type Service struct{ backend Backend }

// NewService returns a Service over b.
func NewService(b Backend) *Service { return &Service{backend: b} }

// Read returns the clipboard content.
func (s *Service) Read(ctx context.Context) (Content, error) {
	c, err := async.Run(ctx, func() (Content, error) { return s.backend.Read(ctx) })
	if c.Formats == nil {
		c.Formats = []string{}
	}
	return c, err
}

// WriteText replaces the clipboard content with text.
func (s *Service) WriteText(ctx context.Context, text string) error {
	_, err := async.Run(ctx, func() (struct{}, error) { return struct{}{}, s.backend.WriteText(ctx, text) })
	return err
}

// WritePNG replaces the clipboard content with a PNG image.
func (s *Service) WritePNG(ctx context.Context, data []byte) error {
	if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "image must be a PNG: %v", err)
	}
	_, err := async.Run(ctx, func() (struct{}, error) { return struct{}{}, s.backend.WritePNG(ctx, data) })
	return err
}
