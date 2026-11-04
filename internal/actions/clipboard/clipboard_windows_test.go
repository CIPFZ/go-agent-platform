//go:build windows

package clipboard

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
	"time"
)

func requireSession(t *testing.T) {
	if os.Getenv("VM_AGENT_INTEGRATION") != "1" {
		t.Skip("set VM_AGENT_INTEGRATION=1 in an interactive session")
	}
}

func TestWin32ClipboardRoundTrips(t *testing.T) {
	requireSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b := newBackend()
	text := "héllo 世界 😀\r\nline"
	if err := b.WriteText(ctx, text); err != nil {
		t.Fatal(err)
	}
	c, err := b.Read(ctx)
	if err != nil || c.Text == nil || *c.Text != text {
		t.Fatalf("text read = %+v %v", c, err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	img.SetNRGBA(1, 1, color.NRGBA{10, 20, 30, 200})
	var buf bytes.Buffer
	png.Encode(&buf, img)
	if err := b.WritePNG(ctx, buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	c, err = b.Read(ctx)
	if err != nil || !bytes.Equal(c.PNG, buf.Bytes()) || c.Text != nil {
		t.Fatalf("image read: %d bytes, text %v, err %v, formats %v", len(c.PNG), c.Text, err, c.Formats)
	}
	hasDIB, hasPNG := false, false
	for _, f := range c.Formats {
		hasDIB = hasDIB || f == "CF_DIB"
		hasPNG = hasPNG || f == "PNG"
	}
	if !hasDIB || !hasPNG {
		t.Fatalf("formats = %v", c.Formats)
	}
}
