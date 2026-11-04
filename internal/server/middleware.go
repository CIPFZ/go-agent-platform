package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/audit"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// info is per-request state shared by the middleware layers and the route
// wrapper. It is created by observe and filled in as the request proceeds.
type info struct {
	start     time.Time
	requestID string
	explicit  bool // X-Request-Id was supplied by the caller
	action    string
	code      string // "ok" or an error code
	dedupHit  bool
	params    map[string]string
	timeout   time.Duration // parsed X-Timeout-Ms, 0 if absent
}

type infoKey struct{}

func reqInfo(ctx context.Context) *info {
	if i, ok := ctx.Value(infoKey{}).(*info); ok {
		return i
	}
	return &info{start: time.Now(), params: map[string]string{}}
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// recover turns a handler panic into an internal error so one bad request
// cannot take the service down.
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Printf("panic serving %s %s: %v\n%s", r.Method, r.URL.Path, v, debug.Stack())
				s.writeError(w, r, protocol.Errorf(protocol.CodeInternal, "internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// observe sets the version header, assigns the request id and writes one
// audit record per request after the response has been flushed.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := &info{start: s.opts.Now(), params: map[string]string{}, action: "unknown"}
		if id := r.Header.Get(protocol.HeaderRequestID); id != "" {
			i.requestID, i.explicit = id, true
		} else {
			i.requestID = newRequestID()
		}
		versionHeader(w)
		// The counter drops only after the audit record is written, so
		// GracefulStop can wait for the records of aborted requests.
		s.stats.inFlight.Add(1)
		defer s.stats.inFlight.Add(-1)
		ctx, cancel := context.WithCancelCause(context.WithValue(r.Context(), infoKey{}, i))
		defer cancel(nil)
		stopAbort := context.AfterFunc(s.base, func() { cancel(context.Cause(s.base)) })
		defer stopAbort()
		next.ServeHTTP(w, r.WithContext(ctx))
		elapsed := s.opts.Now().Sub(i.start)
		_ = http.NewResponseController(w).Flush()
		if i.code == "" {
			i.code = "ok"
		}
		s.stats.observe(i.action, i.code, elapsed, i.dedupHit)
		s.opts.Audit.Record(audit.Event{
			Time:      i.start.UTC(),
			Kind:      "request",
			RequestID: i.requestID,
			Remote:    r.RemoteAddr,
			Action:    i.action,
			Method:    r.Method,
			Path:      r.URL.Path,
			Code:      i.code,
			ElapsedMS: elapsed.Milliseconds(),
			DedupHit:  i.dedupHit,
			Params:    i.params,
		})
	})
}

// validateHeaders checks X-Request-Id and X-Timeout-Ms (spec section 5.3).
func (s *Server) validateHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := reqInfo(r.Context())
		if i.explicit && !protocol.RequestIDPattern.MatchString(i.requestID) {
			bad := i.requestID
			i.requestID = newRequestID()
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "X-Request-Id %q must be 16 to 128 characters of [A-Za-z0-9._-]", truncate(bad, 160)).WithReason(protocol.ReasonInvalidRequestID))
			return
		}
		if raw := r.Header.Get(protocol.HeaderTimeoutMS); raw != "" {
			ms, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || ms <= 0 {
				s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "X-Timeout-Ms must be a positive integer number of milliseconds").WithReason(protocol.ReasonInvalidTimeout))
				return
			}
			if ms > int64(TransferTimeout/time.Millisecond) {
				ms = int64(TransferTimeout / time.Millisecond)
			}
			i.timeout = time.Duration(ms) * time.Millisecond
		}
		next.ServeHTTP(w, r)
	})
}

// limit enforces -max-concurrency; excess requests get busy (spec 5.7).
func (s *Server) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.limiter <- struct{}{}:
			defer func() { <-s.limiter }()
			next.ServeHTTP(w, r)
		default:
			s.stats.busy.Add(1)
			if _, pattern := s.mux.Handler(r); pattern != "" {
				reqInfo(r.Context()).action = s.actionFor(pattern)
			}
			s.writeError(w, r, protocol.Errorf(protocol.CodeBusy, "concurrency limit of %d in-flight requests reached", s.opts.MaxConcurrency))
		}
	})
}

func (s *Server) actionFor(pattern string) string {
	for _, rt := range s.routes {
		if rt.Method+" "+rt.Path == pattern {
			return rt.Action
		}
	}
	return "unknown"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return fmt.Sprintf("%s...", s[:n])
}
