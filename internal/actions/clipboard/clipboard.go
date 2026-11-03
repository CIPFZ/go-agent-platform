// Package clipboard implements /v1/clipboard for text and PNG images.
package clipboard

// Content is what the clipboard currently offers.
type Content struct {
	Formats []string
	Text    *string
	PNG     []byte
}

// Backend reads and writes the system clipboard.
type Backend interface {
	Read() (Content, error)
	WriteText(text string) error
	WritePNG(png []byte) error
}

// NewBackend returns the backend for the current platform and session.
func NewBackend() Backend { return newBackend() }
