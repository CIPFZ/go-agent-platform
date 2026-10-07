// Package artifacts provides durable storage for task artifact payloads.
package artifacts

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var ErrInvalidKey = errors.New("invalid artifact storage key")

// Store is the gateway's payload storage boundary. Implementations may use a
// local volume or an object-store adapter without changing the HTTP protocol.
type Store interface {
	Put(key string, data []byte) error
	Get(key string) ([]byte, error)
	Delete(key string) error
}

// FileStore stores each object as a mode-0600 file and replaces it atomically.
// The directory must be a private directory on a dedicated local volume.
type FileStore struct{ root string }

func NewFileStore(root string) (*FileStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("artifact storage directory is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &FileStore{root: root}, nil
}

func (s *FileStore) path(key string) (string, error) {
	if key == "" || key == "." || key == ".." || strings.ContainsAny(key, `/\\`) || strings.ContainsRune(key, 0) {
		return "", fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	return filepath.Join(s.root, key), nil
}

func (s *FileStore) Put(key string, data []byte) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".artifact-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (s *FileStore) Get(key string) ([]byte, error) {
	path, err := s.path(key)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return nil, errors.New("invalid artifact object")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, (2<<20)+1))
}

func (s *FileStore) Delete(key string) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
