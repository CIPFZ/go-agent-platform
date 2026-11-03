package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/CIPFZ/go-agent-platform/internal/dedup"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func (s *Server) serveRoute(rt *Route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := reqInfo(r.Context())
		i.action = rt.Action
		c := &Call{Request: r, Writer: w, route: rt, info: i, server: s}
		defer c.release()

		var key string
		if rt.Class == NonRetryable && i.explicit && s.opts.Dedup != nil {
			key = r.Method + " " + r.URL.Path + " " + i.requestID
			switch state, body := s.opts.Dedup.Begin(key); state {
			case dedup.InFlight:
				s.stats.dedupInFlight.Add(1)
				s.writeError(w, r, protocol.Errorf(protocol.CodeConflict, "a request with this X-Request-Id is still executing").WithReason(protocol.ReasonInFlight))
				return
			case dedup.Stream:
				s.writeError(w, r, protocol.Errorf(protocol.CodeConflict, "the first response for this X-Request-Id was streamed and cannot be replayed").WithReason(protocol.ReasonDedupStream))
				return
			case dedup.Done:
				i.dedupHit = true
				i.code = replayCode(body)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set(protocol.HeaderDedupHit, "true")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
				return
			}
		}

		result, err := s.invoke(c)
		if stream, ok := result.(*Stream); ok && err == nil {
			if key != "" {
				s.opts.Dedup.FinishStream(key)
			}
			s.writeStream(w, stream)
			return
		}
		var body []byte
		var status int
		if err != nil {
			perr := toError(err, rt)
			i.code = string(perr.Code)
			body, status = s.encode(protocol.Response{OK: false, RequestID: i.requestID, Error: perr}), perr.Code.HTTPStatus()
		} else {
			if result == nil {
				result = protocol.Empty{}
			}
			elapsed := s.opts.Now().Sub(i.start).Milliseconds()
			body, status = s.encode(protocol.Response{OK: true, RequestID: i.requestID, ElapsedMS: &elapsed, Result: result}), http.StatusOK
		}
		if key != "" {
			s.opts.Dedup.Finish(key, body)
		}
		writeJSON(w, status, body)
	})
}

func (s *Server) invoke(c *Call) (result any, err error) {
	defer func() {
		if v := recover(); v != nil {
			if v == http.ErrAbortHandler {
				panic(v)
			}
			err = protocol.Errorf(protocol.CodeInternal, "internal error: %v", v)
		}
	}()
	return c.route.Handler(c)
}

func (s *Server) encode(resp protocol.Response) []byte {
	body, err := json.Marshal(resp)
	if err != nil {
		body, _ = json.Marshal(protocol.Response{OK: false, RequestID: resp.RequestID, Error: protocol.Errorf(protocol.CodeInternal, "encode response: %v", err)})
	}
	return append(body, '\n')
}

func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, perr *protocol.Error) {
	i := reqInfo(r.Context())
	i.code = string(perr.Code)
	writeJSON(w, perr.Code.HTTPStatus(), s.encode(protocol.Response{OK: false, RequestID: i.requestID, Error: perr}))
}

func (s *Server) writeStream(w http.ResponseWriter, st *Stream) {
	if closer, ok := st.Body.(io.Closer); ok {
		defer closer.Close()
	}
	h := w.Header()
	h.Set("Content-Type", st.ContentType)
	for k, v := range st.Header {
		h.Set(k, v)
	}
	if st.Size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(st.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, st.Body)
}

// replayCode extracts the outcome of a cached response for the audit record.
func replayCode(body []byte) string {
	var resp struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &resp) == nil && !resp.OK && resp.Error != nil {
		return resp.Error.Code
	}
	return "ok"
}

// toError maps any handler error to a spec 5.5 error object.
func toError(err error, rt *Route) *protocol.Error {
	var perr *protocol.Error
	switch {
	case errors.As(err, &perr):
		if perr.Code == protocol.CodeTimeout {
			perr.Retryable = rt.Class == Retryable
		}
		return perr
	case errors.Is(err, context.DeadlineExceeded):
		return &protocol.Error{Code: protocol.CodeTimeout, Message: "action timed out", Retryable: rt.Class == Retryable}
	case errors.Is(err, context.Canceled):
		e := &protocol.Error{Code: protocol.CodeTimeout, Message: "the client disconnected before the action finished", Retryable: rt.Class == Retryable}
		return e.WithReason(protocol.ReasonClientDisconnected)
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return protocol.Errorf(protocol.CodeTooLarge, "request body exceeds %d bytes", tooLarge.Limit)
	}
	return IOError(err)
}

// IOError classifies a filesystem or subprocess error (spec 5.5).
func IOError(err error) *protocol.Error {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return perr
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return protocol.Errorf(protocol.CodeNotFound, "%v", err)
	case errors.Is(err, fs.ErrExist):
		return protocol.Errorf(protocol.CodeConflict, "%v", err)
	case errors.Is(err, syscall.ENOSPC):
		return protocol.Errorf(protocol.CodeIOFailed, "%v", err).WithReason(protocol.ReasonDiskFull)
	case errors.Is(err, fs.ErrPermission):
		return protocol.Errorf(protocol.CodeIOFailed, "%v", err).WithReason(protocol.ReasonPermissionDenied)
	}
	var pathErr *fs.PathError
	var linkErr *os.LinkError
	var sysErr *os.SyscallError
	var execErr *exec.Error
	var exitErr *exec.ExitError
	if errors.As(err, &pathErr) || errors.As(err, &linkErr) || errors.As(err, &sysErr) || errors.As(err, &execErr) || errors.As(err, &exitErr) {
		return protocol.Errorf(protocol.CodeIOFailed, "%v", err)
	}
	return protocol.Errorf(protocol.CodeInternal, "%v", err)
}
