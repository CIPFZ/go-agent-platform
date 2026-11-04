package file

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Remove deletes p; a non-empty directory needs recursive.
func (f *FS) Remove(p string, recursive bool) error {
	info, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if info.IsDir() && !recursive {
		if err := os.Remove(p); err != nil {
			if entries, readErr := os.ReadDir(p); readErr == nil && len(entries) > 0 {
				return protocol.Errorf(protocol.CodeConflict, "%s is a non-empty directory; set recursive to remove it", p)
			}
			return err
		}
		return nil
	}
	if info.IsDir() {
		return os.RemoveAll(p)
	}
	return os.Remove(p)
}

// prepareDestination applies overwrite to an existing destination: without
// it the call fails with conflict, with it the destination (file or whole
// directory) is removed first. from and to must differ.
func prepareDestination(from, to string, overwrite bool) error {
	if samePath(from, to) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "from and to are the same path")
	}
	if within(from, to) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "to lies inside from")
	}
	if _, err := os.Lstat(to); err == nil {
		if !overwrite {
			return protocol.Errorf(protocol.CodeConflict, "%s already exists; set overwrite to replace it", to)
		}
		return os.RemoveAll(to)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(filepath.Dir(to)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return protocol.Errorf(protocol.CodeNotFound, "parent directory of %s does not exist", to)
		}
		return err
	}
	return nil
}

// Move renames from to to, copying and deleting when they are on different
// volumes.
func (f *FS) Move(from, to string, overwrite bool) error {
	if _, err := os.Lstat(from); err != nil {
		return err
	}
	if err := prepareDestination(from, to, overwrite); err != nil {
		return err
	}
	err := os.Rename(from, to)
	if err == nil {
		return nil
	}
	if !isCrossDevice(err) {
		return err
	}
	if err := copyTree(from, to); err != nil {
		_ = os.RemoveAll(to)
		return err
	}
	return os.RemoveAll(from)
}

func isCrossDevice(err error) bool {
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return errors.Is(linkErr.Err, syscall.EXDEV) || isWindowsNotSameDevice(linkErr.Err)
	}
	return false
}

// Copy copies a file (atomically) or a directory tree from from to to.
// Symbolic links are copied as links.
func (f *FS) Copy(from, to string, overwrite bool) error {
	if _, err := os.Lstat(from); err != nil {
		return err
	}
	if err := prepareDestination(from, to, overwrite); err != nil {
		return err
	}
	if err := copyTree(from, to); err != nil {
		_ = os.RemoveAll(to)
		return err
	}
	return nil
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		case d.IsDir():
			return os.MkdirAll(dst, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			return copyFile(path, dst, info.Mode().Perm())
		default:
			return nil // sockets, devices and pipes are skipped
		}
	})
}

func copyFile(from, to string, perm fs.FileMode) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	if _, err := WriteAtomic(to, src, false); err != nil {
		return err
	}
	return os.Chmod(to, perm)
}
