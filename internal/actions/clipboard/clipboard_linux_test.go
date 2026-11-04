//go:build linux

package clipboard

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// These tests need an X server and xclip; they run with
// VM_AGENT_INTEGRATION=1, like test/integration.
func requireX(t *testing.T) {
	t.Helper()
	if os.Getenv("VM_AGENT_INTEGRATION") != "1" || os.Getenv("DISPLAY") == "" {
		t.Skip("set VM_AGENT_INTEGRATION=1 and DISPLAY")
	}
	if _, err := exec.LookPath("xclip"); err != nil {
		t.Skip("xclip is not installed")
	}
}

func ctx5() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	time.AfterFunc(5*time.Second, cancel)
	return ctx
}

func xclipOut(t *testing.T, target string) []byte {
	t.Helper()
	out, err := exec.Command("xclip", "-o", "-selection", "clipboard", "-t", target).Output()
	if err != nil {
		t.Fatalf("xclip -o %s: %v", target, err)
	}
	return out
}

func xclipIn(t *testing.T, target string, data []byte) {
	t.Helper()
	cmd := exec.Command("xclip", "-i", "-selection", "clipboard", "-t", target)
	cmd.Stdin = bytes.NewReader(data)
	if err := cmd.Run(); err != nil {
		t.Fatalf("xclip -i: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
}

func noisyPNG(t *testing.T, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(1))
	for i := range img.Pix {
		img.Pix[i] = byte(r.Intn(256))
	}
	img.SetRGBA(0, 0, color.RGBA{1, 2, 3, 255})
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func TestWriteTextReadByOtherClient(t *testing.T) {
	requireX(t)
	b := newBackend()
	text := "héllo 世界\nsecond line"
	if err := b.WriteText(ctx5(), text); err != nil {
		t.Fatal(err)
	}
	if got := string(xclipOut(t, "UTF8_STRING")); got != text {
		t.Fatalf("xclip read %q", got)
	}
	targets := string(xclipOut(t, "TARGETS"))
	for _, want := range []string{"UTF8_STRING", "STRING", "TARGETS"} {
		if !strings.Contains(targets, want) {
			t.Fatalf("targets %q lack %s", targets, want)
		}
	}
	c, err := b.Read(ctx5())
	if err != nil || c.Text == nil || *c.Text != text {
		t.Fatalf("self read = %+v %v", c, err)
	}
}

func TestReadFromOtherOwner(t *testing.T) {
	requireX(t)
	b := newBackend()
	xclipIn(t, "UTF8_STRING", []byte("from xclip ✓"))
	c, err := b.Read(ctx5())
	if err != nil || c.Text == nil || *c.Text != "from xclip ✓" || c.PNG != nil {
		t.Fatalf("read = %+v %v", c, err)
	}
	pngData := noisyPNG(t, 40, 30)
	xclipIn(t, "image/png", pngData)
	c, err = b.Read(ctx5())
	if err != nil || !bytes.Equal(c.PNG, pngData) || c.Text != nil {
		t.Fatalf("image read: %d bytes, text %v, err %v, formats %v", len(c.PNG), c.Text, err, c.Formats)
	}
	if !contains(c.Formats, "image/png") {
		t.Fatalf("formats = %v", c.Formats)
	}
}

// Images larger than one request use the INCR protocol in both directions.
func TestLargeImagesUseIncr(t *testing.T) {
	requireX(t)
	b := newBackend()
	big := noisyPNG(t, 600, 500) // ~1.2 MB of incompressible pixels
	if len(big) < 4*incrChunk {
		t.Fatalf("test image too small: %d", len(big))
	}
	if err := b.WritePNG(ctx5(), big); err != nil {
		t.Fatal(err)
	}
	if got := xclipOut(t, "image/png"); !bytes.Equal(got, big) {
		t.Fatalf("xclip read %d bytes of %d", len(got), len(big))
	}
	xclipIn(t, "image/png", big)
	c, err := b.Read(ctx5())
	if err != nil || !bytes.Equal(c.PNG, big) {
		t.Fatalf("incr read %d of %d bytes: %v", len(c.PNG), len(big), err)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
