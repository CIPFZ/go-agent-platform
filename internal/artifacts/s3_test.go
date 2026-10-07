package artifacts

import (
	"errors"
	"testing"
)

func TestS3StoreValidatesConfigurationAndKeys(t *testing.T) {
	if _, err := NewS3Store(S3Config{}); err == nil {
		t.Fatal("empty S3 configuration accepted")
	}
	store, err := NewS3Store(S3Config{Endpoint: "http://127.0.0.1:9000", Bucket: "agent-artifacts", Prefix: "gateway", AccessKeyFile: "access", SecretKeyFile: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.objectName("a1"); err != nil || got != "gateway/a1" {
		t.Fatalf("object name=%q err=%v", got, err)
	}
	if _, err := store.objectName("../escape"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("path traversal err=%v", err)
	}
}
