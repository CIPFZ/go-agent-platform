package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialFileFailureClosesAPIAndRecovers(t *testing.T) {
	for _, state := range []string{"missing", "empty", "whitespace", "directory", "oversized", "multiline"} {
		t.Run(state, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credential")
			switch state {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "empty", "whitespace", "oversized", "multiline":
				contents := map[string]string{"empty": "", "whitespace": " \n\t", "oversized": strings.Repeat("a", 4097), "multiline": "first\nsecond"}[state]
				if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			h := NewServer(Config{AuthToken: "must-not-fallback", AuthTokenFile: path})
			for _, authorization := range []string{"", "Bearer must-not-fallback", "Bearer "} {
				request := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
				request.Header.Set("Authorization", authorization)
				response := httptest.NewRecorder()
				h.ServeHTTP(response, request)
				if response.Code != http.StatusServiceUnavailable {
					t.Fatalf("unusable credential file: HTTP %d, want 503", response.Code)
				}
				if strings.Contains(response.Body.String(), path) || strings.Contains(response.Body.String(), "must-not-fallback") {
					t.Fatal("authentication response leaked credential details")
				}
			}
			health := httptest.NewRecorder()
			h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if health.Code != http.StatusServiceUnavailable {
				t.Fatalf("health status=%d", health.Code)
			}
			if state != "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte("restored-token\n"), 0600); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
			request.Header.Set("Authorization", "Bearer restored-token")
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("restored credential: HTTP %d", response.Code)
			}
		})
	}
}
