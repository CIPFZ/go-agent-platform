package api

import (
	"encoding/base64"
	"strconv"
	"unicode/utf8"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
)

const timeoutClipboard = timeoutScreen // spec section 7: clipboard 5 s

func (a *API) clipboardRoutes() []*server.Route {
	return []*server.Route{
		{Method: "GET", Path: "/v1/clipboard", Action: "clipboard.read", Class: server.Retryable, Handler: a.clipboardRead},
		{Method: "POST", Path: "/v1/clipboard", Action: "clipboard.write", Class: server.NonRetryable, Handler: a.clipboardWrite},
	}
}

func (a *API) clipboardRead(c *server.Call) (any, error) {
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	content, err := a.clipboard.Read(c.Context(timeoutClipboard))
	if err != nil {
		return nil, a.displayError(err)
	}
	res := protocol.ClipboardResult{Formats: content.Formats, Text: content.Text}
	if content.PNG != nil {
		img := base64.StdEncoding.EncodeToString(content.PNG)
		res.Image = &img
	}
	return res, nil
}

func (a *API) clipboardWrite(c *server.Call) (any, error) {
	var req protocol.ClipboardWriteRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	if (req.Text == nil) == (req.Image == nil) {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "give exactly one of text or image")
	}
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	ctx := c.Context(timeoutClipboard)
	var err error
	if req.Text != nil {
		c.Param("kind", "text")
		c.Param("chars", strconv.Itoa(utf8.RuneCountInString(*req.Text)))
		err = a.clipboard.WriteText(ctx, *req.Text)
	} else {
		data, decErr := base64.StdEncoding.DecodeString(*req.Image)
		if decErr != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "image must be base64: %v", decErr)
		}
		c.Param("kind", "image")
		c.Param("bytes", strconv.Itoa(len(data)))
		err = a.clipboard.WritePNG(ctx, data)
	}
	if err != nil {
		return nil, a.displayError(err)
	}
	return protocol.Empty{}, nil
}
