package api

import (
	"bytes"
	"encoding/base64"
	"strconv"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/screen"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
)

func (a *API) screenCapture(c *server.Call) (any, error) {
	var req protocol.CaptureRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	if err := validEncoding(req.Encoding); err != nil {
		return nil, err
	}
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	opts := screen.Options{Region: req.Region, Cursor: req.Cursor, Format: req.Format, Quality: req.Quality}
	if req.Display != nil {
		opts.Display = *req.Display
	}
	shot, err := a.screen.Capture(c.Context(timeoutScreen), opts)
	if err != nil {
		return nil, a.displayError(err)
	}
	c.Param("width", strconv.Itoa(shot.Width))
	c.Param("height", strconv.Itoa(shot.Height))
	c.Param("sha256", shot.SHA256)
	if req.Encoding == "stream" {
		return &server.Stream{
			ContentType: "image/" + shot.Format,
			Size:        int64(len(shot.Data)),
			Header: map[string]string{
				protocol.HeaderResultWidth:  strconv.Itoa(shot.Width),
				protocol.HeaderResultHeight: strconv.Itoa(shot.Height),
				protocol.HeaderResultSHA256: shot.SHA256,
				protocol.HeaderResultSize:   strconv.Itoa(len(shot.Data)),
				protocol.HeaderCursorX:      strconv.Itoa(shot.Cursor.X),
				protocol.HeaderCursorY:      strconv.Itoa(shot.Cursor.Y),
				protocol.HeaderCapturedAt:   shot.CapturedAt.UTC().Format(time.RFC3339Nano),
			},
			Body: bytes.NewReader(shot.Data),
		}, nil
	}
	return protocol.CaptureResult{
		Image:      base64.StdEncoding.EncodeToString(shot.Data),
		Format:     shot.Format,
		Width:      shot.Width,
		Height:     shot.Height,
		Cursor:     shot.Cursor,
		CapturedAt: shot.CapturedAt.UTC(),
		SHA256:     shot.SHA256,
	}, nil
}

func (a *API) screenInfo(c *server.Call) (any, error) {
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	displays, err := a.screen.Displays(c.Context(timeoutScreen))
	if err != nil {
		return nil, a.displayError(err)
	}
	return map[string]any{"displays": displays}, nil
}

func (a *API) screenCursor(c *server.Call) (any, error) {
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	cursor, err := a.screen.Cursor(c.Context(timeoutScreen))
	if err != nil {
		return nil, a.displayError(err)
	}
	return cursor, nil
}

// displayError reports a failure of a display-bound backend as no_display
// when the session went away meanwhile, and as io_failed otherwise.
func (a *API) displayError(err error) error {
	var perr *protocol.Error
	if asProtocolError(err, &perr) {
		return perr
	}
	if isContextError(err) {
		return err
	}
	if st := a.session(); !st.DisplayReady || st.Locked {
		return protocol.Errorf(protocol.CodeNoDisplay, "the graphical session became unavailable: %v", err)
	}
	return protocol.Errorf(protocol.CodeIOFailed, "%v", err)
}

func validEncoding(e string) error {
	switch e {
	case "", "base64", "stream":
		return nil
	}
	return protocol.Errorf(protocol.CodeInvalidRequest, "encoding must be base64 or stream")
}
