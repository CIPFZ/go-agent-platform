package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/file"
	"github.com/CIPFZ/go-agent-platform/internal/async"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
)

// Default timeouts of spec section 7.
const (
	timeoutFile     = 30 * time.Second
	timeoutFileLong = 60 * time.Second // file/read and file/remove
	timeoutTransfer = 10 * time.Minute // file/download and file/upload
)

func (a *API) fileRoutes() []*server.Route {
	return []*server.Route{
		{Method: "POST", Path: "/v1/file/read", Action: "file.read", Class: server.Retryable, Handler: a.fileRead},
		{Method: "POST", Path: "/v1/file/write", Action: "file.write", Class: server.NonRetryable, Handler: a.fileWrite},
		{Method: "POST", Path: "/v1/file/stat", Action: "file.stat", Class: server.Retryable, Handler: a.fileStat},
		{Method: "POST", Path: "/v1/file/list", Action: "file.list", Class: server.Retryable, Handler: a.fileList},
		{Method: "POST", Path: "/v1/file/mkdir", Action: "file.mkdir", Class: server.NonRetryable, Handler: a.fileMkdir},
		{Method: "GET", Path: "/v1/file/known_dirs", Action: "file.known_dirs", Class: server.Retryable, Handler: a.fileKnownDirs},
		{Method: "POST", Path: "/v1/file/remove", Action: "file.remove", Class: server.NonRetryable, Handler: a.fileRemove},
		{Method: "POST", Path: "/v1/file/move", Action: "file.move", Class: server.NonRetryable, Handler: a.fileMove},
		{Method: "POST", Path: "/v1/file/copy", Action: "file.copy", Class: server.NonRetryable, Handler: a.fileCopy},
		{Method: "POST", Path: "/v1/file/download", Action: "file.download", Class: server.NonRetryable, Transfer: true, Handler: a.fileDownload},
		{Method: "POST", Path: "/v1/file/upload", Action: "file.upload", Class: server.NonRetryable, Transfer: true, Handler: a.fileUpload},
	}
}

// path validates a request path and records it for the audit log.
func (a *API) path(c *server.Call, key, p string) (string, error) {
	c.Param(key, p)
	return a.files.Path(p)
}

func (a *API) fileRead(c *server.Call) (any, error) {
	var req protocol.FileReadRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	if err := validEncoding(req.Encoding); err != nil {
		return nil, err
	}
	p, err := a.path(c, "path", req.Path)
	if err != nil {
		return nil, err
	}
	content, err := async.Run(c.Context(timeoutFileLong), func() (*file.Content, error) { return a.files.Read(p, req.Offset, req.Limit) })
	if err != nil {
		return nil, err
	}
	c.Param("size", strconv.Itoa(len(content.Data)))
	mtime := content.Mtime.UTC()
	if req.Encoding == "stream" {
		return &server.Stream{
			ContentType: "application/octet-stream",
			Size:        int64(len(content.Data)),
			Header: map[string]string{
				protocol.HeaderResultSize:   strconv.Itoa(len(content.Data)),
				protocol.HeaderResultSHA256: content.SHA256,
				protocol.HeaderFileMtime:    mtime.Format(time.RFC3339Nano),
			},
			Body: bytes.NewReader(content.Data),
		}, nil
	}
	return protocol.FileReadResult{
		Content: base64.StdEncoding.EncodeToString(content.Data),
		Size:    int64(len(content.Data)),
		SHA256:  content.SHA256,
		Mtime:   mtime,
	}, nil
}

func (a *API) fileWrite(c *server.Call) (any, error) {
	var req protocol.FileWriteRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	p, err := a.path(c, "path", req.Path)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(req.Content)
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "content must be base64: %v", err)
	}
	c.Param("size", strconv.Itoa(len(data)))
	opts := file.WriteOptions{Overwrite: req.Overwrite, Mkdir: req.Mkdir, SHA256: req.SHA256}
	return async.Run(c.Context(timeoutFile), func() (*file.WriteResult, error) { return a.files.Write(p, data, opts) })
}

func (a *API) fileStat(c *server.Call) (any, error) {
	var req protocol.FilePathRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	p, err := a.path(c, "path", req.Path)
	if err != nil {
		return nil, err
	}
	return async.Run(c.Context(timeoutFile), func() (*file.Info, error) { return a.files.Stat(p) })
}

func (a *API) fileList(c *server.Call) (any, error) {
	var req protocol.FileListRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	p, err := a.path(c, "path", req.Path)
	if err != nil {
		return nil, err
	}
	limit := file.DefaultMaxList
	if req.MaxEntries != nil {
		limit = *req.MaxEntries
	}
	if limit < 1 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "max_entries must be positive")
	}
	type listing struct {
		Entries   []file.Entry `json:"entries"`
		Truncated bool         `json:"truncated"`
	}
	return async.Run(c.Context(timeoutFile), func() (listing, error) {
		entries, truncated, err := a.files.List(p, req.Recursive, limit)
		return listing{entries, truncated}, err
	})
}

func (a *API) fileMkdir(c *server.Call) (any, error) {
	var req protocol.FilePathRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	p, err := a.path(c, "path", req.Path)
	if err != nil {
		return nil, err
	}
	_, err = async.Run(c.Context(timeoutFile), func() (struct{}, error) { return struct{}{}, a.files.Mkdir(p) })
	return protocol.Empty{}, err
}

func (a *API) fileKnownDirs(c *server.Call) (any, error) {
	return async.Run(c.Context(timeoutSystem), file.KnownDirs)
}

func (a *API) fileRemove(c *server.Call) (any, error) {
	var req protocol.FileRemoveRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	p, err := a.path(c, "path", req.Path)
	if err != nil {
		return nil, err
	}
	if req.Recursive {
		c.Param("recursive", "true")
	}
	_, err = async.Run(c.Context(timeoutFileLong), func() (struct{}, error) { return struct{}{}, a.files.Remove(p, req.Recursive) })
	return protocol.Empty{}, err
}

func (a *API) fileMoveOrCopy(c *server.Call, op func(from, to string, overwrite bool) error) (any, error) {
	var req protocol.FileMoveRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	from, err := a.path(c, "from", req.From)
	if err != nil {
		return nil, err
	}
	to, err := a.path(c, "to", req.To)
	if err != nil {
		return nil, err
	}
	_, err = async.Run(c.Context(timeoutFile), func() (struct{}, error) { return struct{}{}, op(from, to, req.Overwrite) })
	return protocol.Empty{}, err
}

func (a *API) fileMove(c *server.Call) (any, error) { return a.fileMoveOrCopy(c, a.files.Move) }
func (a *API) fileCopy(c *server.Call) (any, error) { return a.fileMoveOrCopy(c, a.files.Copy) }

func (a *API) fileDownload(c *server.Call) (any, error) {
	var req protocol.FileDownloadRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	c.Param("url", req.URL)
	if err := file.ValidateDownloadURL(req.URL); err != nil {
		return nil, err
	}
	p, err := a.path(c, "path", req.Path)
	if err != nil {
		return nil, err
	}
	dl := file.DownloadRequest{URL: req.URL, Path: p, MaxBytes: file.DownloadMax, Retries: file.DefaultRetries, Headers: req.Headers}
	if req.MaxBytes != nil {
		if *req.MaxBytes <= 0 || *req.MaxBytes > file.DownloadMax {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "max_bytes must be between 1 and %d", file.DownloadMax)
		}
		dl.MaxBytes = *req.MaxBytes
	}
	if req.Retries != nil {
		if *req.Retries < 0 || *req.Retries > 10 {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "retries must be between 0 and 10")
		}
		dl.Retries = *req.Retries
	}
	if len(req.Headers) > 0 {
		keys := make([]string, 0, len(req.Headers))
		for k := range req.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		c.Param("header_keys", strings.Join(keys, ","))
	}
	res, err := a.files.Download(c.Context(timeoutTransfer), a.downloader, dl)
	if err != nil {
		return nil, err
	}
	c.Param("size", strconv.FormatInt(res.Size, 10))
	return res, nil
}

func (a *API) fileUpload(c *server.Call) (any, error) {
	ct := c.Request.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "file/upload needs a multipart/form-data body")
	}
	ctx := c.Context(timeoutTransfer)
	body := http.MaxBytesReader(c.Writer, c.Request.Body, server.MultipartBodyMax)
	// Reads observe the deadline: closing the body unblocks a stalled read.
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	results, err := a.files.Upload(multipart.NewReader(body, params["boundary"]), func(data []byte) (file.UploadOptions, error) {
		var o file.UploadOptions
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&o); err != nil {
			return o, protocol.Errorf(protocol.CodeInvalidRequest, "invalid options part: %v", err).WithReason(protocol.ReasonInvalidJSON)
		}
		return o, nil
	})
	paths := make([]string, len(results))
	for i, r := range results {
		paths[i] = r.Path
	}
	c.Param("paths", strings.Join(paths, ","))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || strings.Contains(err.Error(), "request body too large") {
			return nil, protocol.Errorf(protocol.CodeTooLarge, "multipart body exceeds %d bytes", server.MultipartBodyMax)
		}
		return nil, err
	}
	return map[string]any{"files": results}, nil
}
