package agent

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type credentialTransport struct {
	requests int
	header   string
}

func (c *credentialTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.requests++
	c.header = r.Header.Get("Authorization")
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"agent":{"agent_id":"test"}}`)), Header: make(http.Header)}, nil
}

func TestClientCredentialFileDoesNotSendUnauthenticatedRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	transport := &credentialTransport{}
	client, err := NewClient(Config{GatewayURL: "http://gateway.invalid", AgentID: "test", AgentVersion: "test", AuthToken: "must-not-fallback", AuthTokenFile: path, HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	for _, contents := range []string{"missing", "", " \t\n", "a\nb", strings.Repeat("x", 4097)} {
		if contents != "missing" {
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
		}
		_, err := client.Register(context.Background())
		if err == nil {
			t.Fatal("unusable credential file accepted")
		}
		if transport.requests != 0 {
			t.Fatal("sent a request despite unusable credentials")
		}
		if !temporary(err) {
			t.Fatal("credential availability must be retryable for recovery")
		}
	}
	for _, token := range []string{"first", "second"} {
		if err := os.WriteFile(path+".next", []byte(token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".next", path); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Register(context.Background()); err != nil {
			t.Fatal(err)
		}
		if transport.header != "Bearer "+token {
			t.Fatal("client did not reload rotated token")
		}
	}
}
