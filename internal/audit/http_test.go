package audit

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestHTTPSinkPostsRotatingToken(t *testing.T) {
	tokenFile := t.TempDir() + "/token"
	if err := os.WriteFile(tokenFile, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := make(chan *http.Request, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	sink, err := NewHTTPSink(server.URL, tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	sink.Record(Event{Kind: "first"})
	if err := os.WriteFile(tokenFile, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink.Record(Event{Kind: "second"})
	for _, want := range []string{"Bearer first", "Bearer second"} {
		request := <-requests
		if request.Header.Get("Authorization") != want {
			t.Fatalf("authorization=%q want=%q", request.Header.Get("Authorization"), want)
		}
	}
}
