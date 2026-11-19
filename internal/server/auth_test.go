package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAccessToken(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("s3cret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	ran := 0
	srv := New(Options{AuthTokenFile: file, Now: func() time.Time { return now }}, []*Route{
		{Method: "POST", Path: "/v1/act", Action: "test.act", Class: NonRetryable, Handler: func(*Call) (any, error) { ran++; return nil, nil }},
		{Method: "GET", Path: "/page/", Action: "test.page", Public: true, Handler: func(*Call) (any, error) { return nil, nil }},
	})
	call := func(method, path, addr, auth string) (int, string, http.Header) {
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader("{}"))
		r.RemoteAddr = addr + ":5000"
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w.Code, w.Body.String(), w.Header()
	}
	reason := func(body, want string) bool { return strings.Contains(body, `"reason":"`+want+`"`) }

	// Without the token nothing runs; the version header is still sent, which
	// the page uses to notice an upgrade.
	code, body, h := call("POST", "/v1/act", "10.0.0.1", "")
	if code != 401 || !reason(body, "token_missing") || ran != 0 || h.Get("X-Agent-Version") == "" || !strings.HasPrefix(h.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("no token: %d %s %v", code, body, h)
	}
	if code, body, _ = call("POST", "/v1/act", "10.0.0.1", "Bearer nope"); code != 401 || !reason(body, "token_wrong") || ran != 0 {
		t.Fatalf("wrong token: %d %s", code, body)
	}
	// Unknown paths do not reveal themselves either.
	if code, _, _ = call("GET", "/v1/nothing", "10.0.0.1", ""); code != 401 {
		t.Fatalf("unknown path without token: %d", code)
	}
	// The public route needs none; the right token works, in any case of the scheme.
	if code, _, _ = call("GET", "/page/", "10.0.0.1", ""); code != 200 {
		t.Fatalf("public route: %d", code)
	}
	if code, _, _ = call("POST", "/v1/act", "10.0.0.1", "bearer s3cret-token"); code != 200 || ran != 1 {
		t.Fatalf("right token: %d ran %d", code, ran)
	}

	// Replacing the file changes the token at once.
	if err := os.WriteFile(file, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ = call("POST", "/v1/act", "10.0.0.1", "Bearer s3cret-token"); code != 401 {
		t.Fatalf("old token after rotation: %d", code)
	}
	if code, _, _ = call("POST", "/v1/act", "10.0.0.1", "Bearer rotated"); code != 200 {
		t.Fatalf("new token: %d", code)
	}

	// Ten wrong tokens lock an address out for a minute, the right token
	// included; other addresses are not affected.
	for i := 0; i < authFailureLimit; i++ {
		call("POST", "/v1/act", "10.0.0.2", "Bearer guess")
	}
	if code, body, _ = call("POST", "/v1/act", "10.0.0.2", "Bearer rotated"); code != 401 || !reason(body, "too_many_attempts") {
		t.Fatalf("locked out address: %d %s", code, body)
	}
	if code, _, _ = call("POST", "/v1/act", "10.0.0.3", "Bearer rotated"); code != 200 {
		t.Fatalf("other address during a lockout: %d", code)
	}
	now = now.Add(authLockout + time.Second)
	if code, _, _ = call("POST", "/v1/act", "10.0.0.2", "Bearer rotated"); code != 200 {
		t.Fatalf("after the lockout: %d", code)
	}

	// A token file that cannot be read refuses everyone.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if code, body, _ = call("POST", "/v1/act", "10.0.0.3", "Bearer rotated"); code != 401 || !reason(body, "token_unavailable") {
		t.Fatalf("missing token file: %d %s", code, body)
	}
}

func TestNoTokenFileMeansNoAuthentication(t *testing.T) {
	srv := New(Options{}, []*Route{{Method: "GET", Path: "/v1/x", Action: "x", Handler: func(*Call) (any, error) { return nil, nil }}})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1/v1/x", nil))
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
}
