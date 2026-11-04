package file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// DownloadRequest is a validated file/download request.
type DownloadRequest struct {
	URL      string
	Path     string
	MaxBytes int64
	Retries  int
	Headers  map[string]string
}

// Downloader fetches URLs; its client is replaceable in tests.
type Downloader struct {
	Client *http.Client
	Sleep  func(ctx context.Context, d time.Duration) error
}

// NewDownloader returns a downloader following at most MaxRedirects
// redirects. It has no address filtering: the agent's network access is
// that of the desktop user (spec 6.6).
func NewDownloader() *Downloader {
	return &Downloader{
		Client: &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return fmt.Errorf("stopped after %d redirects", MaxRedirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
			}
			return nil
		}},
		Sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
}

// ValidateDownloadURL accepts only absolute http and https URLs.
func ValidateDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "url must be an absolute http or https URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "url scheme %q is not allowed; use http or https", u.Scheme).WithReason(protocol.ReasonURLScheme)
	}
	return nil
}

// errPermanent marks failures that retrying cannot fix; any other attempt
// error is retried.
type errPermanent struct{ err error }

func (e errPermanent) Error() string { return e.err.Error() }
func (e errPermanent) Unwrap() error { return e.err }

// Download fetches req.URL into req.Path. Every attempt shares ctx's
// deadline; transport errors, 408, 429 and 5xx are retried with backoff,
// other statuses fail at once. The file is published atomically and never
// replaces an existing file.
func (f *FS) Download(ctx context.Context, d *Downloader, req DownloadRequest) (*WriteResult, error) {
	if err := f.prepareTarget(req.Path, WriteOptions{}); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(req.Path); err == nil {
		return nil, protocol.Errorf(protocol.CodeConflict, "%s already exists", req.Path)
	}
	var last error
	for attempt := 0; attempt <= req.Retries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<min(attempt-1, 4)) * time.Second
			if err := d.Sleep(ctx, backoff); err != nil {
				return nil, errors.Join(err, last)
			}
		}
		res, err := f.downloadOnce(ctx, d, req)
		if err == nil {
			return res, nil
		}
		last = err
		var perm errPermanent
		if errors.As(err, &perm) || ctx.Err() != nil {
			break
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var perm errPermanent
	if errors.As(last, &perm) {
		return nil, perm.err
	}
	return nil, last
}

func (f *FS) downloadOnce(ctx context.Context, d *Downloader, req DownloadRequest) (*WriteResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return nil, errPermanent{protocol.Errorf(protocol.CodeInvalidRequest, "%v", err)}
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := d.Client.Do(httpReq)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) && strings.Contains(urlErr.Err.Error(), "redirect") {
			return nil, errPermanent{protocol.Errorf(protocol.CodeIOFailed, "%v", err)}
		}
		return nil, protocol.Errorf(protocol.CodeIOFailed, "download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		e := protocol.Errorf(protocol.CodeIOFailed, "download: server answered %s", resp.Status).WithDetail("http_status", resp.StatusCode)
		if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, e
		}
		return nil, errPermanent{e}
	}
	if resp.ContentLength > req.MaxBytes {
		return nil, errPermanent{protocol.Errorf(protocol.CodeTooLarge, "the response has %d bytes, more than max_bytes %d", resp.ContentLength, req.MaxBytes)}
	}
	h := sha256.New()
	limited := &limitReader{r: resp.Body, left: req.MaxBytes}
	n, err := WriteAtomic(req.Path, io.TeeReader(limited, h), false)
	if limited.exceeded {
		return nil, errPermanent{protocol.Errorf(protocol.CodeTooLarge, "the response exceeds max_bytes %d", req.MaxBytes)}
	}
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, errPermanent{writeError(req.Path, err)}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, protocol.Errorf(protocol.CodeIOFailed, "download: %v", err)
	}
	return &WriteResult{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

type limitReader struct {
	r        io.Reader
	left     int64
	exceeded bool
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		var one [1]byte
		if n, _ := l.r.Read(one[:]); n > 0 {
			l.exceeded = true
			return 0, errors.New("max_bytes exceeded")
		}
		return 0, io.EOF
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}

// UploadResult is one uploaded file.
type UploadResult struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// UploadOptions is the JSON of the optional "options" part.
type UploadOptions struct {
	Overwrite bool `json:"overwrite"`
	Mkdir     bool `json:"mkdir"`
}

// Upload streams the parts of a multipart body to disk. Each file part's
// form name is its absolute target path; an optional part named "options"
// (JSON) applies to every file and must come before them. Each file is
// published atomically on its own; a failure stops the upload and leaves
// the files already published in place.
func (f *FS) Upload(r *multipart.Reader, decodeOptions func([]byte) (UploadOptions, error)) ([]UploadResult, error) {
	var opts UploadOptions
	results := []UploadResult{}
	sawFile := false
	for {
		part, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return results, protocol.Errorf(protocol.CodeInvalidRequest, "read multipart body: %v", err)
		}
		name := part.FormName()
		if name == "options" {
			if sawFile {
				return results, protocol.Errorf(protocol.CodeInvalidRequest, "the options part must precede the file parts")
			}
			data, err := io.ReadAll(io.LimitReader(part, 64<<10))
			if err != nil {
				return results, err
			}
			if opts, err = decodeOptions(data); err != nil {
				return results, err
			}
			continue
		}
		sawFile = true
		p, err := f.Path(name)
		if err != nil {
			return results, err
		}
		if err := f.prepareTarget(p, WriteOptions{Overwrite: opts.Overwrite, Mkdir: opts.Mkdir}); err != nil {
			return results, err
		}
		h := sha256.New()
		n, err := WriteAtomic(p, io.TeeReader(part, h), opts.Overwrite)
		if err != nil {
			return results, writeError(p, err)
		}
		results = append(results, UploadResult{Path: p, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
	}
	if !sawFile {
		return results, protocol.Errorf(protocol.CodeInvalidRequest, "the upload contains no file part")
	}
	return results, nil
}
