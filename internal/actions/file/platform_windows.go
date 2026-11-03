//go:build windows

package file

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

func attributes(p string) uint32 {
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return 0
	}
	a, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return 0
	}
	return a
}

func isHidden(p string, _ fs.FileInfo) bool {
	return attributes(p)&windows.FILE_ATTRIBUTE_HIDDEN != 0
}

func isReadonly(p string, _ fs.FileInfo) bool {
	return attributes(p)&windows.FILE_ATTRIBUTE_READONLY != 0
}

func freeBytes(p string) (int64, error) {
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &avail, &total, &free); err != nil {
		return 0, err
	}
	return int64(avail), nil
}

// KnownDirs resolves the special directories with SHGetKnownFolderPath
// (spec section 10).
func KnownDirs() (map[string]string, error) {
	dirs := map[string]string{"temp": os.TempDir()}
	for name, id := range map[string]*windows.KNOWNFOLDERID{
		"home":      windows.FOLDERID_Profile,
		"desktop":   windows.FOLDERID_Desktop,
		"documents": windows.FOLDERID_Documents,
		"downloads": windows.FOLDERID_Downloads,
		"pictures":  windows.FOLDERID_Pictures,
	} {
		p, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT)
		if err != nil {
			return nil, err
		}
		dirs[name] = p
	}
	return dirs, nil
}
