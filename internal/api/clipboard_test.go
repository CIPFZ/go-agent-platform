package api

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"testing"

	"github.com/CIPFZ/go-agent-platform/internal/actions/session"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func TestClipboardEndpoints(t *testing.T) {
	e := newEnv(t)
	var res protocol.ClipboardResult
	e.call("GET", "/v1/clipboard", nil).into(t, &res)
	if res.Formats == nil || len(res.Formats) != 0 || res.Text != nil || res.Image != nil {
		t.Fatalf("empty clipboard = %+v", res)
	}
	e.call("POST", "/v1/clipboard", map[string]any{"text": "copied ✓"}).into(t, &map[string]any{})
	e.call("GET", "/v1/clipboard", nil).into(t, &res)
	if res.Text == nil || *res.Text != "copied ✓" {
		t.Fatalf("text = %+v", res)
	}
	if last := e.lastAudit("clipboard.write"); last.Params["chars"] != "8" || last.Params["kind"] != "text" {
		t.Fatalf("audit = %v", last.Params)
	}
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	img := base64.StdEncoding.EncodeToString(buf.Bytes())
	e.call("POST", "/v1/clipboard", map[string]any{"image": img}).into(t, &map[string]any{})
	e.call("GET", "/v1/clipboard", nil).into(t, &res)
	if res.Image == nil || *res.Image != img {
		t.Fatalf("image = %+v", res)
	}
	e.call("POST", "/v1/clipboard", map[string]any{"image": base64.StdEncoding.EncodeToString([]byte("not png"))}).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/clipboard", map[string]any{"text": "a", "image": img}).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/clipboard", `{}`).code(t, protocol.CodeInvalidRequest)
	e.setSession(session.State{DisplayReady: true, Locked: true})
	e.call("GET", "/v1/clipboard", nil).code(t, protocol.CodeNoDisplay)
}
