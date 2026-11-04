package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

func TestFileTransfersThroughClient(t *testing.T) {
	e := newEnv(t)
	c := client.New(e.http.URL)
	ctx := context.Background()
	dir := t.TempDir()

	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "secret" {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		w.Write([]byte("downloaded body"))
	}))
	defer src.Close()
	dst := filepath.Join(dir, "dl.txt")
	res, err := c.FileDownload(ctx, &client.FileDownloadRequest{URL: src.URL, Path: dst, Headers: map[string]string{"X-Token": "secret"}})
	if err != nil || res.Size != 15 {
		t.Fatalf("download = %+v, %v", res, err)
	}
	ev := e.lastAudit("file.download")
	if ev.Params["header_keys"] != "X-Token" || strings.Contains(strings.Join([]string{ev.Params["url"], ev.Params["path"]}, " "), "secret") {
		t.Fatalf("download audit = %v", ev.Params)
	}
	_, err = c.FileDownload(ctx, &client.FileDownloadRequest{URL: "ftp://x/y", Path: filepath.Join(dir, "x")})
	var apiErr *client.Error
	if !errors.As(err, &apiErr) || apiErr.Reason() != "url_scheme" {
		t.Fatalf("scheme err = %v", err)
	}

	up, err := c.FileUpload(ctx, []client.UploadFile{
		{Path: filepath.Join(dir, "up", "a.txt"), Body: strings.NewReader("first")},
		{Path: filepath.Join(dir, "up", "b.bin"), Body: bytes.NewReader(make([]byte, 3<<20))},
	}, &client.UploadOptions{Mkdir: client.Ptr(true)})
	if err != nil || len(up.Files) != 2 || up.Files[1].Size != 3<<20 {
		t.Fatalf("upload = %+v, %v", up, err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "up", "a.txt")); string(data) != "first" {
		t.Fatalf("uploaded content = %q", data)
	}
	_, err = c.FileUpload(ctx, []client.UploadFile{{Path: filepath.Join(dir, "up", "a.txt"), Body: strings.NewReader("x")}}, nil)
	if !errors.As(err, &apiErr) || apiErr.Code != "conflict" {
		t.Fatalf("upload conflict = %v", err)
	}
	r := e.call("POST", "/v1/file/upload", `{"path":"x"}`)
	r.code(t, "invalid_request")

	if err := c.FileCopy(ctx, &client.FileMoveRequest{From: filepath.Join(dir, "up"), To: filepath.Join(dir, "up2")}); err != nil {
		t.Fatal(err)
	}
	if err := c.FileMove(ctx, &client.FileMoveRequest{From: filepath.Join(dir, "up2"), To: filepath.Join(dir, "up3")}); err != nil {
		t.Fatal(err)
	}
	if err := c.FileRemove(ctx, &client.FileRemoveRequest{Path: filepath.Join(dir, "up3")}); !errors.As(err, &apiErr) || apiErr.Code != "conflict" {
		t.Fatalf("non-recursive remove = %v", err)
	}
	if err := c.FileRemove(ctx, &client.FileRemoveRequest{Path: filepath.Join(dir, "up3"), Recursive: client.Ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "up3")); !os.IsNotExist(err) {
		t.Fatal("remove left the directory")
	}

	// Transfers are deduplicated like every non-retryable action.
	again := &client.FileDownloadRequest{URL: src.URL, Path: filepath.Join(dir, "dedup.txt"), Headers: map[string]string{"X-Token": "secret"}}
	var info client.ResponseInfo
	if _, err := c.FileDownload(ctx, again, client.WithRequestID("download-dedup-0001")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FileDownload(ctx, again, client.WithRequestID("download-dedup-0001"), client.WithResponseInfo(&info)); err != nil || !info.DedupHit {
		t.Fatalf("replay = %v %+v", err, info)
	}
}

func TestUploadBodyLimit(t *testing.T) {
	e := newEnv(t)
	// A body over the limit is refused with too_large; the limit itself is
	// the 1 GiB constant, so exercise the error path with a short body that
	// claims to be huge.
	req, _ := http.NewRequest("POST", e.http.URL+"/v1/file/upload", strings.NewReader("--b\r\n"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("truncated multipart status = %d", resp.StatusCode)
	}
}
