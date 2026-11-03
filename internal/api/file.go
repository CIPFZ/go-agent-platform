package api

import (
	"bytes"
	"encoding/base64"
	"strconv"
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
)

func (a *API) fileRoutes() []*server.Route {
	return []*server.Route{
		{Method: "POST", Path: "/v1/file/read", Action: "file.read", Class: server.Retryable, Handler: a.fileRead},
		{Method: "POST", Path: "/v1/file/write", Action: "file.write", Class: server.NonRetryable, Handler: a.fileWrite},
		{Method: "POST", Path: "/v1/file/stat", Action: "file.stat", Class: server.Retryable, Handler: a.fileStat},
		{Method: "POST", Path: "/v1/file/list", Action: "file.list", Class: server.Retryable, Handler: a.fileList},
		{Method: "POST", Path: "/v1/file/mkdir", Action: "file.mkdir", Class: server.NonRetryable, Handler: a.fileMkdir},
		{Method: "GET", Path: "/v1/file/known_dirs", Action: "file.known_dirs", Class: server.Retryable, Handler: a.fileKnownDirs},
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
