package main

import (
	"bytes"
	"os"
	"testing"
)

// pkg/client/client_gen.go must be regenerated whenever docs/openapi.yaml
// changes: cd tools && go run ./clientgen
func TestGeneratedClientIsUpToDate(t *testing.T) {
	spec, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want, err := Generate(spec, "client")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../pkg/client/client_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("pkg/client/client_gen.go is stale; run: cd tools && go run ./clientgen")
	}
}

func TestGoName(t *testing.T) {
	for in, want := range map[string]string{"visible_only": "VisibleOnly", "screenCapture": "ScreenCapture", "job_id": "JobID", "dx": "DX", "argv_sha256": "ArgvSHA256", "X-Request-Id": "XRequestID"} {
		if got := goName(in); got != want {
			t.Errorf("goName(%q) = %q, want %q", in, got, want)
		}
	}
}
