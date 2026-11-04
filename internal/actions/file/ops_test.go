package file

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func codeOf(err error) protocol.Code {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return perr.Code
	}
	return ""
}

func TestRemove(t *testing.T) {
	f, _ := New(nil)
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	os.WriteFile(file, []byte("x"), 0o600)
	if err := f.Remove(file, false); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(file, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing err = %v", err)
	}
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(filepath.Join(sub, "deep"), 0o755)
	if codeOf(f.Remove(sub, false)) != protocol.CodeConflict {
		t.Fatal("non-empty directory removed without recursive")
	}
	if err := f.Remove(sub, true); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	os.Mkdir(empty, 0o755)
	if err := f.Remove(empty, false); err != nil {
		t.Fatalf("empty dir: %v", err)
	}
}

func TestMoveAndCopy(t *testing.T) {
	f, _ := New(nil)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	os.MkdirAll(filepath.Join(src, "a"), 0o755)
	os.WriteFile(filepath.Join(src, "a", "f.txt"), []byte("one"), 0o640)
	os.WriteFile(filepath.Join(src, "top.txt"), []byte("top"), 0o600)
	if runtime.GOOS != "windows" {
		os.Symlink("top.txt", filepath.Join(src, "link"))
	}

	cp := filepath.Join(dir, "copy")
	if err := f.Copy(src, cp, false); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(cp, "a", "f.txt")); string(data) != "one" {
		t.Fatalf("copied content = %q", data)
	}
	if runtime.GOOS != "windows" {
		if target, err := os.Readlink(filepath.Join(cp, "link")); err != nil || target != "top.txt" {
			t.Fatalf("link = %q %v", target, err)
		}
		if info, _ := os.Stat(filepath.Join(cp, "a", "f.txt")); info.Mode().Perm() != 0o640 {
			t.Fatalf("mode = %v", info.Mode())
		}
	}
	if codeOf(f.Copy(src, cp, false)) != protocol.CodeConflict {
		t.Fatal("copy over existing without overwrite")
	}
	os.WriteFile(filepath.Join(cp, "stale"), nil, 0o600)
	if err := f.Copy(src, cp, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cp, "stale")); !os.IsNotExist(err) {
		t.Fatal("overwrite must replace the whole destination")
	}
	if codeOf(f.Copy(src, filepath.Join(src, "inside"), false)) != protocol.CodeInvalidRequest {
		t.Fatal("copy into itself accepted")
	}
	if codeOf(f.Copy(src, src, true)) != protocol.CodeInvalidRequest {
		t.Fatal("copy onto itself accepted")
	}

	mv := filepath.Join(dir, "moved")
	if err := f.Move(cp, mv, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cp); !os.IsNotExist(err) {
		t.Fatal("move left the source")
	}
	if data, _ := os.ReadFile(filepath.Join(mv, "top.txt")); string(data) != "top" {
		t.Fatalf("moved content = %q", data)
	}
	file := filepath.Join(dir, "single.txt")
	os.WriteFile(file, []byte("s"), 0o600)
	if codeOf(f.Move(file, filepath.Join(mv, "top.txt"), false)) != protocol.CodeConflict {
		t.Fatal("move over existing without overwrite")
	}
	if err := f.Move(file, filepath.Join(mv, "top.txt"), true); err != nil {
		t.Fatal(err)
	}
	if codeOf(f.Move(filepath.Join(dir, "nope"), filepath.Join(dir, "x"), false)) != "" {
		if err := f.Move(filepath.Join(dir, "nope"), filepath.Join(dir, "x"), false); !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if codeOf(f.Move(filepath.Join(mv, "top.txt"), filepath.Join(dir, "missing", "x"), false)) != protocol.CodeNotFound {
		t.Fatal("move into a missing directory")
	}
}

func TestCopyTreeHandlesCrossDeviceFallback(t *testing.T) {
	// Move's cross-volume fallback is copyTree followed by RemoveAll; check
	// copyTree directly since tests cannot rely on two volumes.
	dir := t.TempDir()
	src := filepath.Join(dir, "s")
	os.MkdirAll(filepath.Join(src, "x", "y"), 0o755)
	os.WriteFile(filepath.Join(src, "x", "y", "z"), []byte("deep"), 0o600)
	if err := copyTree(src, filepath.Join(dir, "d")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "d", "x", "y", "z")); string(data) != "deep" {
		t.Fatalf("data = %q", data)
	}
}

func instantDownloader() *Downloader {
	d := NewDownloader()
	d.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return d
}

func TestDownload(t *testing.T) {
	payload := bytes.Repeat([]byte("payload-"), 1000)
	sum := sha256.Sum256(payload)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/ok":
			if r.Header.Get("Authorization") != "Bearer t" {
				http.Error(w, "no auth", 401)
				return
			}
			w.Write(payload)
		case "/flaky":
			if hits.Load()%3 != 0 {
				http.Error(w, "busy", 503)
				return
			}
			w.Write(payload)
		case "/missing":
			http.NotFound(w, r)
		case "/redirect":
			n := 0
			fmt.Sscan(r.URL.Query().Get("n"), &n)
			if n == 0 {
				w.Write(payload)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/redirect?n=%d", n-1), http.StatusFound)
		case "/big":
			w.Write(bytes.Repeat([]byte("x"), 5000))
		case "/big-chunked":
			w.Header().Set("Transfer-Encoding", "chunked")
			for i := 0; i < 5; i++ {
				w.Write(bytes.Repeat([]byte("x"), 1000))
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer srv.Close()
	f, _ := New(nil)
	d := instantDownloader()
	dir := t.TempDir()
	req := func(path, name string) DownloadRequest {
		return DownloadRequest{URL: srv.URL + path, Path: filepath.Join(dir, name), MaxBytes: DownloadMax, Retries: DefaultRetries, Headers: map[string]string{"Authorization": "Bearer t"}}
	}
	res, err := f.Download(context.Background(), d, req("/ok", "a"))
	if err != nil || res.Size != int64(len(payload)) || res.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("download = %+v, %v", res, err)
	}
	if _, err := f.Download(context.Background(), d, req("/ok", "a")); codeOf(err) != protocol.CodeConflict {
		t.Fatalf("existing target err = %v", err)
	}
	hits.Store(0)
	if _, err := f.Download(context.Background(), d, req("/flaky", "flaky")); err != nil || hits.Load() != 3 {
		t.Fatalf("retry: err=%v hits=%d", err, hits.Load())
	}
	hits.Store(0)
	r := req("/flaky", "flaky2")
	r.Retries = 1
	if _, err := f.Download(context.Background(), d, r); codeOf(err) != protocol.CodeIOFailed || hits.Load() != 2 {
		t.Fatalf("retries exhausted: err=%v hits=%d", err, hits.Load())
	}
	hits.Store(0)
	if _, err := f.Download(context.Background(), d, req("/missing", "m")); codeOf(err) != protocol.CodeIOFailed || hits.Load() != 1 {
		t.Fatalf("404 must not be retried: err=%v hits=%d", err, hits.Load())
	}
	if _, err := f.Download(context.Background(), d, req("/redirect?n=10", "r10")); err != nil {
		t.Fatalf("10 redirects: %v", err)
	}
	if _, err := f.Download(context.Background(), d, req("/redirect?n=11", "r11")); err == nil {
		t.Fatal("11 redirects followed")
	}
	for _, p := range []string{"/big", "/big-chunked"} {
		r := req(p, strings.TrimPrefix(p, "/"))
		r.MaxBytes = 4096
		if _, err := f.Download(context.Background(), d, r); codeOf(err) != protocol.CodeTooLarge {
			t.Fatalf("%s over max_bytes: %v", p, err)
		}
		if _, err := os.Stat(r.Path); !os.IsNotExist(err) {
			t.Fatalf("%s: partial file left behind", p)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	slow := NewDownloader() // real backoff: the shared deadline must stop it
	r = req("/missing-host", "slow")
	r.URL = "http://127.0.0.1:1/never"
	if _, err := f.Download(ctx, slow, r); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline err = %v", err)
	}
	for _, bad := range []string{"ftp://x/y", "file:///etc/passwd", "not a url", "/relative"} {
		if err := ValidateDownloadURL(bad); codeOf(err) != protocol.CodeInvalidRequest {
			t.Errorf("%q accepted", bad)
		}
	}
}

func multipartBody(t *testing.T, parts ...[2]string) (*multipart.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range parts {
		var fw io.Writer
		var err error
		if p[0] == "options" {
			fw, err = w.CreateFormField("options")
		} else {
			fw, err = w.CreateFormFile(p[0], filepath.Base(p[0]))
		}
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte(p[1]))
	}
	w.Close()
	return multipart.NewReader(&buf, w.Boundary()), w.Boundary()
}

func decodeOpts(data []byte) (UploadOptions, error) {
	var o UploadOptions
	err := json.Unmarshal(data, &o)
	return o, err
}

func TestUpload(t *testing.T) {
	f, _ := New(nil)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "x", "a.txt"), filepath.Join(dir, "x", "b.bin")
	r, _ := multipartBody(t, [2]string{"options", `{"mkdir":true}`}, [2]string{a, "alpha"}, [2]string{b, "beta"})
	res, err := f.Upload(r, decodeOpts)
	if err != nil || len(res) != 2 || res[0].Path != a || res[1].Size != 4 {
		t.Fatalf("upload = %+v, %v", res, err)
	}
	if data, _ := os.ReadFile(b); string(data) != "beta" {
		t.Fatalf("b = %q", data)
	}
	r, _ = multipartBody(t, [2]string{a, "again"})
	if _, err := f.Upload(r, decodeOpts); codeOf(err) != protocol.CodeConflict {
		t.Fatalf("no-overwrite err = %v", err)
	}
	r, _ = multipartBody(t, [2]string{"options", `{"overwrite":true}`}, [2]string{a, "again"})
	if _, err := f.Upload(r, decodeOpts); err != nil {
		t.Fatal(err)
	}
	r, _ = multipartBody(t, [2]string{filepath.Join(dir, "x", "c.txt"), "x"}, [2]string{"options", `{"overwrite":true}`})
	if _, err := f.Upload(r, decodeOpts); codeOf(err) != protocol.CodeInvalidRequest {
		t.Fatalf("late options err = %v", err)
	}
	r, _ = multipartBody(t, [2]string{"relative.txt", "x"})
	if _, err := f.Upload(r, decodeOpts); codeOf(err) != protocol.CodeInvalidRequest {
		t.Fatalf("relative name err = %v", err)
	}
	r, _ = multipartBody(t, [2]string{"options", `{}`})
	if _, err := f.Upload(r, decodeOpts); codeOf(err) != protocol.CodeInvalidRequest {
		t.Fatalf("no files err = %v", err)
	}
	r, _ = multipartBody(t, [2]string{filepath.Join(dir, "nodir", "f"), "x"})
	if _, err := f.Upload(r, decodeOpts); codeOf(err) != protocol.CodeNotFound {
		t.Fatalf("missing parent err = %v", err)
	}
}
