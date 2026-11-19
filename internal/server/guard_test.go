package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGuardRefusesForeignOriginsAndHostNames(t *testing.T) {
	ran := 0
	srv := New(Options{AllowedHosts: []string{"Desk.Lan"}}, []*Route{
		{Method: "POST", Path: "/v1/act", Action: "test.act", Class: NonRetryable, Handler: func(*Call) (any, error) { ran++; return nil, nil }},
	})
	for _, c := range []struct {
		name, host, origin string
		status             int
		reason             string
	}{
		{"api client by address", "192.168.1.2:7070", "", 200, ""},
		{"api client by IPv6 address", "[fe80::1%eth0]:7070", "", 200, ""},
		{"localhost", "localhost:7070", "", 200, ""},
		{"allowed name, any case", "desk.lan:7070", "", 200, ""},
		{"own page", "192.168.1.2:7070", "http://192.168.1.2:7070", 200, ""},
		{"another site", "192.168.1.2:7070", "http://evil.example", 400, "foreign_origin"},
		{"same address, other port", "192.168.1.2:7070", "http://192.168.1.2:8080", 400, "foreign_origin"},
		{"sandboxed page", "192.168.1.2:7070", "null", 400, "foreign_origin"},
		{"rebound name", "evil.example:7070", "", 400, "unknown_host"},
		{"rebound name posing as same origin", "evil.example:7070", "http://evil.example:7070", 400, "unknown_host"},
	} {
		before := ran
		r := httptest.NewRequest("POST", "/v1/act", strings.NewReader("{}"))
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != c.status || (c.reason != "" && !strings.Contains(w.Body.String(), `"reason":"`+c.reason+`"`)) {
			t.Errorf("%s: status %d body %s", c.name, w.Code, w.Body.String())
		}
		if executed := ran != before; executed != (c.status == http.StatusOK) {
			t.Errorf("%s: handler executed = %v", c.name, executed)
		}
	}
}
