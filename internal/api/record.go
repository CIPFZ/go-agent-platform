package api

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/file"
	"github.com/CIPFZ/go-agent-platform/internal/actions/record"
	"github.com/CIPFZ/go-agent-platform/internal/actions/screen"
	"github.com/CIPFZ/go-agent-platform/internal/async"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
)

// Default timeouts of spec section 7.
const (
	timeoutRecord   = 10 * time.Second // record/start and record/stop
	timeoutDownload = 10 * time.Minute // record/{id} download; capped at 1 h
)

// Defaults of spec 6.8.
const (
	defaultFPS    = 10
	defaultPreset = "ultrafast"
	defaultCRF    = 28
)

func (a *API) recordRoutes() []*server.Route {
	return []*server.Route{
		{Method: "POST", Path: "/v1/record/start", Action: "record.start", Class: server.NonRetryable, Handler: a.recordStart},
		{Method: "POST", Path: "/v1/record/stop", Action: "record.stop", Class: server.NonRetryable, Handler: a.recordStop},
		{Method: "GET", Path: "/v1/record/list", Action: "record.list", Class: server.Retryable, Handler: a.recordList},
		{Method: "GET", Path: "/v1/record/{session_id}", Action: "record.download", Class: server.Retryable, Transfer: true, Handler: a.recordDownload},
		{Method: "DELETE", Path: "/v1/record/{session_id}", Action: "record.delete", Class: server.NonRetryable, Handler: a.recordDelete},
		{Method: "POST", Path: "/v1/record/{session_id}/upload", Action: "record.upload", Class: server.NonRetryable, Transfer: true, Handler: a.recordUpload},
	}
}

// recordUpload copies a finished recording to the configured bucket
// (spec 13.3, optional). The key defaults to <session_id>.mp4 under the
// configured prefix.
func (a *API) recordUpload(c *server.Call) (any, error) {
	var req protocol.RecordUploadRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	id := c.Request.PathValue("session_id")
	c.Param("session_id", id)
	if a.uploader == nil {
		return nil, protocol.Unsupported("no object storage is configured (-upload-s3-endpoint)")
	}
	if req.Key == "" {
		req.Key = id + ".mp4"
	}
	key, err := a.uploader.ObjectKey(req.Key)
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "%v", err)
	}
	c.Param("key", key)
	f, s, err := a.recorder.OpenVideo(id)
	if err != nil {
		return nil, recordError(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	res, err := a.uploader.Put(c.Context(timeoutDownload), key, f, info.Size(), "video/mp4")
	if err != nil {
		if isContextError(err) {
			return nil, err
		}
		return nil, protocol.Errorf(protocol.CodeIOFailed, "upload to bucket %s: %v", a.uploader.Bucket(), err)
	}
	c.Param("size", strconv.FormatInt(res.Size, 10))
	return map[string]any{"session_id": id, "bucket": res.Bucket, "key": res.Key, "size": res.Size, "etag": res.ETag, "sha256": s.SHA256}, nil
}

func recordError(err error) error {
	switch {
	case errors.Is(err, record.ErrNoFFmpeg):
		return protocol.Unsupported("%v", err)
	case errors.Is(err, record.ErrActive), errors.Is(err, record.ErrStillActive):
		return protocol.Errorf(protocol.CodeConflict, "%v", err)
	case errors.Is(err, record.ErrNotFound):
		return protocol.Errorf(protocol.CodeNotFound, "%v", err)
	}
	return err
}

func (a *API) recordStart(c *server.Call) (any, error) {
	var req protocol.RecordStartRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	o := record.Options{FPS: defaultFPS, Preset: defaultPreset, CRF: defaultCRF}
	if req.FPS != nil {
		o.FPS = *req.FPS
	}
	if req.Preset != "" {
		o.Preset = req.Preset
	}
	if req.CRF != nil {
		o.CRF = *req.CRF
	}
	switch {
	case o.FPS < 1 || o.FPS > 60:
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "fps must be between 1 and 60")
	case !slices.Contains(record.Presets, o.Preset):
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "preset must be one of %v", record.Presets)
	case o.CRF < 0 || o.CRF > 51:
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "crf must be between 0 and 51")
	}
	sel := screen.Options{Region: req.Region}
	if req.Display != nil {
		sel.Display = *req.Display
	}
	if sel.Display < -1 || (sel.Region != nil && sel.Region.Empty()) {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "display must be -1 or an index and region must have a positive size")
	}
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	if !a.recorder.Available() {
		return nil, recordError(record.ErrNoFFmpeg)
	}
	if err := file.CheckFreeSpace(a.cfg.RecordDir); err != nil {
		return nil, err
	}
	ctx := c.Context(timeoutRecord)
	displays, err := a.screen.Displays(ctx)
	if err != nil {
		return nil, a.displayError(err)
	}
	if o.Rect, err = screen.TargetRect(displays, sel); err != nil {
		return nil, err
	}
	if o.Rect.W < 2 || o.Rect.H < 2 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "the recorded area must be at least 2x2 pixels")
	}
	c.Param("fps", strconv.Itoa(o.FPS))
	c.Param("rect", strconv.Itoa(o.Rect.X)+","+strconv.Itoa(o.Rect.Y)+","+strconv.Itoa(o.Rect.W)+","+strconv.Itoa(o.Rect.H))
	s, err := a.recorder.Start(ctx, o)
	if err != nil {
		if isContextError(err) {
			return nil, err
		}
		return nil, recordError(err)
	}
	c.Param("session_id", s.ID)
	return map[string]any{"session_id": s.ID, "started_at": s.StartedAt}, nil
}

func (a *API) recordStop(c *server.Call) (any, error) {
	var req protocol.RecordStopRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	if req.SessionID == "" {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "session_id is required")
	}
	c.Param("session_id", req.SessionID)
	s, err := async.Run(c.Context(timeoutRecord), func() (*record.Session, error) { return a.recorder.Stop(req.SessionID) })
	if err != nil {
		if isContextError(err) {
			return nil, err
		}
		return nil, recordError(err)
	}
	c.Param("size", strconv.FormatInt(s.Size, 10))
	return map[string]any{"session_id": s.ID, "size": s.Size, "duration_ms": s.DurationMS, "sha256": s.SHA256}, nil
}

func (a *API) recordList(c *server.Call) (any, error) {
	_ = c.Context(timeoutSystem)
	return map[string]any{"sessions": a.recorder.List()}, nil
}

func (a *API) recordDownload(c *server.Call) (any, error) {
	id := c.Request.PathValue("session_id")
	c.Param("session_id", id)
	f, s, err := a.recorder.OpenVideo(id)
	if err != nil {
		return nil, recordError(err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	c.Param("size", strconv.FormatInt(info.Size(), 10))
	return &server.Stream{
		ContentType: "video/mp4",
		Size:        info.Size(),
		Header: map[string]string{
			protocol.HeaderResultSize:   strconv.FormatInt(info.Size(), 10),
			protocol.HeaderResultSHA256: s.SHA256,
		},
		Body: &ctxReader{ctx: c.Context(timeoutDownload), r: f},
	}, nil
}

func (a *API) recordDelete(c *server.Call) (any, error) {
	id := c.Request.PathValue("session_id")
	c.Param("session_id", id)
	if err := a.recorder.Delete(id); err != nil {
		return nil, recordError(err)
	}
	return protocol.Empty{}, nil
}

// ctxReader stops a streamed download when its deadline passes; the
// client then sees a body shorter than Content-Length.
type ctxReader struct {
	ctx context.Context
	r   io.ReadCloser
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func (c *ctxReader) Close() error { return c.r.Close() }
