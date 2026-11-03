//go:build linux

package file

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKnownDirsReadsUserDirs(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".config")
	os.MkdirAll(cfg, 0o755)
	os.WriteFile(filepath.Join(cfg, "user-dirs.dirs"), []byte("# comment\nXDG_DESKTOP_DIR=\"$HOME/桌面\"\nXDG_DOWNLOAD_DIR=\"/data/dl\"\nXDG_MUSIC_DIR=\"$HOME/Music\"\n"), 0o644)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dirs, err := KnownDirs()
	if err != nil {
		t.Fatal(err)
	}
	if dirs["desktop"] != filepath.Join(home, "桌面") || dirs["downloads"] != "/data/dl" || dirs["documents"] != filepath.Join(home, "Documents") || dirs["home"] != home {
		t.Fatalf("dirs = %v", dirs)
	}
}

func TestReadonlyUsesAccess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anything")
	}
	p := filepath.Join(t.TempDir(), "ro")
	os.WriteFile(p, nil, 0o400)
	f, _ := New(nil)
	if info, _ := f.Stat(p); !info.Readonly {
		t.Fatal("0400 file not readonly")
	}
}
