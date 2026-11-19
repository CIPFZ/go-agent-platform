package server

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// guard refuses the two kinds of request a web page can make a browser send
// to vm-agent on the page's behalf. Neither needs the attacker to be on the
// network: the victim only has to open a page.
//
//   - A request from another site. Browsers send such a POST without asking
//     first when it looks like a form submission, and vm-agent would execute
//     it. They always mark it with an Origin header naming the page's site;
//     scripts and API clients send none.
//   - A request under a foreign host name. A site can point its own name at
//     this machine's address (DNS rebinding) so that the browser treats
//     vm-agent as that site, same origin and all. Such a request carries the
//     site's name in Host.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		perr := s.checkHost(r)
		if perr == nil {
			perr = checkOrigin(r)
		}
		if perr != nil {
			if _, pattern := s.mux.Handler(r); pattern != "" {
				reqInfo(r.Context()).action = s.actionFor(pattern)
			}
			s.writeError(w, r, perr)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostName returns the host of a Host header value without port, brackets
// or IPv6 zone, in lower case.
func hostName(hostport string) string {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	if i := strings.IndexByte(h, '%'); i >= 0 {
		h = h[:i]
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// checkHost accepts an address, localhost and the names in
// Options.AllowedHosts. A request without Host (HTTP/1.0) is not a browser's.
func (s *Server) checkHost(r *http.Request) *protocol.Error {
	h := hostName(r.Host)
	if h == "" || h == "localhost" || net.ParseIP(h) != nil || s.hosts[h] {
		return nil
	}
	return protocol.Errorf(protocol.CodeInvalidRequest, "vm-agent is not served under the host name %q; use its address or add the name to -allowed-hosts", truncate(h, 80)).
		WithReason(protocol.ReasonUnknownHost)
}

// checkOrigin accepts a request without Origin, or with the origin it was
// itself addressed to (vm-agent's own /remote/ page).
func checkOrigin(r *http.Request) *protocol.Error {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	if u, err := url.Parse(origin); err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host) {
		return nil
	}
	return protocol.Errorf(protocol.CodeInvalidRequest, "requests from other web origins are refused (Origin %q)", truncate(origin, 80)).
		WithReason(protocol.ReasonForeignOrigin)
}
