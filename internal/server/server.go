// Package server is the vm-agent HTTP layer (spec sections 3 and 5): the
// middleware chain, deduplication, timeouts, the response envelope and the
// route table. Every request is executed synchronously and answered once.
package server

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/audit"
	"github.com/CIPFZ/go-agent-platform/internal/dedup"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/version"
)

// Spec limits (sections 5.1, 5.3 and 7).
const (
	JSONBodyMax      int64 = 8 << 20
	MultipartBodyMax int64 = 1 << 30
	SyncTimeoutMax         = 10 * time.Minute
	TransferTimeout        = time.Hour
	ReadHeaderLimit        = 10 * time.Second
	IdleLimit              = 120 * time.Second
	MaxHeaderBytes         = 64 << 10
)

// Middleware wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Passthrough is the only authentication implementation (spec 5.2, D9): it
// reserves the position in the chain and forwards every request unchanged.
func Passthrough(next http.Handler) http.Handler { return next }

// Options configures a Server.
type Options struct {
	Audit          audit.Sink
	Dedup          *dedup.Cache // nil disables deduplication
	MaxConcurrency int
	// Authenticate occupies the authentication slot; nil means Passthrough.
	Authenticate Middleware
	// AuditVerbose records full argv and typed text.
	AuditVerbose bool
	Now          func() time.Time
}

// Server routes /v1 requests to handlers.
type Server struct {
	opts    Options
	mux     *http.ServeMux
	routes  []*Route
	limiter chan struct{}
	handler http.Handler
	stats   *Stats
}

// Class is the retry class of an action (spec section 5.6).
type Class int

const (
	// Retryable actions may be retried by callers; they are never deduplicated
	// and their timeouts are reported as retryable.
	Retryable Class = iota
	// NonRetryable actions have side effects; requests with X-Request-Id are
	// deduplicated.
	NonRetryable
)

// Route is one endpoint.
type Route struct {
	Method string
	Path   string // net/http pattern path, e.g. /v1/exec/jobs/{job_id}
	// Action names the endpoint in audit records and metrics, e.g. "screen.capture".
	Action string
	Class  Class
	// Transfer routes use the 1 hour timeout cap instead of 10 minutes.
	Transfer bool
	Handler  HandlerFunc
}

// HandlerFunc executes one request. It returns a JSON-encodable result, a
// *Stream, or an error (preferably a *protocol.Error).
type HandlerFunc func(c *Call) (any, error)

// New creates a server with the given routes.
func New(opts Options, routes []*Route) *Server {
	if opts.Audit == nil {
		opts.Audit = audit.NewMemorySink(1)
	}
	if opts.MaxConcurrency <= 0 {
		opts.MaxConcurrency = 32
	}
	if opts.Authenticate == nil {
		opts.Authenticate = Passthrough
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &Server{opts: opts, mux: http.NewServeMux(), limiter: make(chan struct{}, opts.MaxConcurrency), stats: newStats()}
	for _, rt := range routes {
		s.routes = append(s.routes, rt)
		s.mux.Handle(rt.Method+" "+rt.Path, s.serveRoute(rt))
	}
	s.mux.HandleFunc("/", s.serveUnknown)
	// Spec section 3: middleware -> dedup -> router -> handler. Dedup needs
	// the route's retry class, so it runs inside the per-route wrapper.
	s.handler = s.recover(s.observe(s.validateHeaders(opts.Authenticate(s.limit(s.mux)))))
	return s
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler { return s.handler }

// Stats exposes the counters behind /v1/metrics.
func (s *Server) Stats() *Stats { return s.stats }

// Routes returns "METHOD path" for every route, sorted.
func (s *Server) Routes() []string {
	out := make([]string, 0, len(s.routes))
	for _, rt := range s.routes {
		out = append(out, rt.Method+" "+rt.Path)
	}
	sort.Strings(out)
	return out
}

// NewHTTPServer returns an http.Server with the parameters of spec 5.3.
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: ReadHeaderLimit,
		IdleTimeout:       IdleLimit,
		MaxHeaderBytes:    MaxHeaderBytes,
	}
}

func (s *Server) serveUnknown(w http.ResponseWriter, r *http.Request) {
	reqInfo(r.Context()).action = "unknown"
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodPut} {
		if method == r.Method {
			continue
		}
		probe := r.Clone(context.Background())
		probe.Method = method
		if _, pattern := s.mux.Handler(probe); pattern != "" && pattern != "/" {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "method %s not allowed for %s", r.Method, r.URL.Path).WithReason(protocol.ReasonMethodNotAllowed))
			return
		}
	}
	s.writeError(w, r, protocol.Errorf(protocol.CodeNotFound, "no route for %s %s", r.Method, r.URL.Path).WithReason(protocol.ReasonUnknownRoute))
}

func versionHeader(w http.ResponseWriter) {
	w.Header().Set(protocol.HeaderAgentVersion, version.Version)
}
