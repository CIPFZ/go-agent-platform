package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/credentials"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Limits on wrong tokens: after authFailureLimit in a row from one address
// that address is refused for authLockout. A random token cannot be guessed;
// this is for tokens people chose themselves.
const (
	authFailureLimit = 10
	authLockout      = time.Minute
	authTrackedMax   = 4096
)

// authenticator checks the access token of every request to a route that is
// not public (docs/remote-desktop-design.md 5.15).
type authenticator struct {
	// tokenFile holds the token. It is read for every request, so replacing
	// the file changes the token without a restart.
	tokenFile string
	now       func() time.Time

	mu       sync.Mutex
	failures map[string]*authFailures
	warned   time.Time
}

type authFailures struct {
	count int
	until time.Time
}

func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// lockedOut reports whether addr has used up its attempts.
func (a *authenticator) lockedOut(addr string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.failures[addr]
	if f == nil {
		return false
	}
	if f.count >= authFailureLimit && a.now().After(f.until) {
		delete(a.failures, addr)
		return false
	}
	return f.count >= authFailureLimit
}

func (a *authenticator) failed(addr string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.failures) >= authTrackedMax {
		// Forgetting everything is the cheap way to bound the table; it can
		// only make the lockout more lenient.
		a.failures = map[string]*authFailures{}
	}
	f := a.failures[addr]
	if f == nil {
		f = &authFailures{}
		a.failures[addr] = f
	}
	if f.count++; f.count >= authFailureLimit {
		f.until = a.now().Add(authLockout)
	}
}

func (a *authenticator) succeeded(addr string) {
	a.mu.Lock()
	delete(a.failures, addr)
	a.mu.Unlock()
}

// bearer extracts the token of an Authorization: Bearer header.
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// check returns nil when r carries the token.
func (a *authenticator) check(r *http.Request) *protocol.Error {
	addr := clientAddress(r)
	if a.lockedOut(addr) {
		return protocol.Errorf(protocol.CodeUnauthorized, "too many wrong access tokens from this address; try again in a minute").
			WithReason(protocol.ReasonTooManyAttempts)
	}
	want, err := credentials.Resolve("", a.tokenFile)
	if err != nil {
		// An unreadable token file refuses everyone rather than letting
		// everyone in.
		a.mu.Lock()
		if a.now().Sub(a.warned) > time.Minute {
			a.warned = a.now()
			log.Printf("vm-agent: the access token file cannot be read; every request is refused")
		}
		a.mu.Unlock()
		return protocol.Errorf(protocol.CodeUnauthorized, "the access token is not available on the server").WithReason(protocol.ReasonTokenUnavailable)
	}
	got := bearer(r)
	if got == "" {
		return protocol.Errorf(protocol.CodeUnauthorized, "an access token is required: send it as \"Authorization: Bearer <token>\"").
			WithReason(protocol.ReasonTokenMissing)
	}
	// Hashing first makes the comparison independent of the lengths.
	g, w := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	if subtle.ConstantTimeCompare(g[:], w[:]) != 1 {
		a.failed(addr)
		return protocol.Errorf(protocol.CodeUnauthorized, "the access token is wrong").WithReason(protocol.ReasonTokenWrong)
	}
	a.succeeded(addr)
	return nil
}

// authenticate is the middleware in the authentication slot when a token
// file is configured. Public routes (the remote desktop page, which holds
// no data) pass without a token.
func (s *Server) authenticate(a *authenticator) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, pattern := s.mux.Handler(r)
			rt := s.routeFor(pattern)
			if rt != nil && rt.Public {
				next.ServeHTTP(w, r)
				return
			}
			if perr := a.check(r); perr != nil {
				if rt != nil {
					reqInfo(r.Context()).action = rt.Action
				}
				w.Header().Set("WWW-Authenticate", `Bearer realm="vm-agent"`)
				s.writeError(w, r, perr)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) routeFor(pattern string) *Route {
	for _, rt := range s.routes {
		if rt.Method+" "+rt.Path == pattern {
			return rt
		}
	}
	return nil
}
