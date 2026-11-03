// Package file implements the /v1/file endpoints on the local filesystem.
package file

import (
	"io"
	"os"
	"path/filepath"
)

// WriteAtomic publishes the complete content of r at path in one filesystem
// operation: data goes to a temporary file in the same directory, is synced,
// and is then renamed over the target (overwrite) or hard-linked into place.
// Link gives atomic create-if-absent semantics even when several writers race
// for the same path; it fails with an error satisfying os.IsExist when the
// target already exists. It returns the number of bytes written.
func WriteAtomic(path string, r io.Reader, overwrite bool) (int64, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vm-agent-write-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, r)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return n, err
	}
	if overwrite {
		return n, os.Rename(tmp.Name(), path)
	}
	return n, os.Link(tmp.Name(), path)
}
