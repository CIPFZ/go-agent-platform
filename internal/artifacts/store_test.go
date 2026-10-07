package artifacts

import (
	"errors"
	"os"
	"testing"
)

func TestFileStoreRoundTripAndBoundary(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put("artifact-1", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("artifact-1")
	if err != nil || string(got) != "payload" {
		t.Fatalf("payload=%q err=%v", got, err)
	}
	if _, err := store.Get("../escape"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("path escape err=%v", err)
	}
	if err := store.Delete("artifact-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.root + "/artifact-1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("object still exists: %v", err)
	}
}
