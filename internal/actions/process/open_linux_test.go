//go:build linux

package process

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSplitExec(t *testing.T) {
	cases := map[string][]string{
		`mousepad %U`:                    {"mousepad", "%U"},
		`"/opt/My App/bin/app" --new %f`: {"/opt/My App/bin/app", "--new", "%f"},
		`sh -c "echo \"hi\" \$HOME" %u`:  {"sh", "-c", `echo "hi" $HOME`, "%u"},
		`  spaced   args  `:              {"spaced", "args"},
	}
	for in, want := range cases {
		got, err := splitExec(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("splitExec(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := splitExec(`"open`); err == nil {
		t.Error("unterminated quote accepted")
	}
}

// A fake XDG tree: text/plain -> fake-editor.desktop, whose Exec writes the
// argument it received to a marker file.
func TestOpenResolvesDefaultApplication(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	cfg := filepath.Join(root, "config")
	bin := filepath.Join(root, "bin")
	for _, d := range []string{filepath.Join(data, "applications"), filepath.Join(data, "mime"), cfg, bin} {
		os.MkdirAll(d, 0o755)
	}
	marker := filepath.Join(root, "opened")
	script := filepath.Join(bin, "fake-editor")
	os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$1\" > "+marker+"\n"), 0o755)
	os.WriteFile(filepath.Join(data, "applications", "fake-editor.desktop"), []byte("[Desktop Entry]\nType=Application\nName=Fake\nExec=fake-editor %f\n"), 0o644)
	os.WriteFile(filepath.Join(data, "applications", "fake-browser.desktop"), []byte("[Desktop Entry]\nExec=\"fake-editor\" --url %u\n"), 0o644)
	os.WriteFile(filepath.Join(cfg, "mimeapps.list"), []byte("[Default Applications]\ntext/plain=missing.desktop;fake-editor.desktop\nx-scheme-handler/https=fake-browser.desktop\n"), 0o644)
	os.WriteFile(filepath.Join(data, "mime", "globs2"), []byte("50:text/plain:*.txt\n"), 0o644)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_DATA_DIRS", data)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CONFIG_DIRS", cfg)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	doc := filepath.Join(root, "note.txt")
	os.WriteFile(doc, []byte("hello"), 0o644)

	res, err := Open(doc, false, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if res.Handler != "fake-editor.desktop" || res.PID == 0 {
		t.Fatalf("result = %+v", res)
	}
	waitFor(t, marker, doc)
	// The launched handler is in its own session, not vm-agent's group.
	if sid, _ := unix.Getsid(res.PID); sid == mySid() {
		t.Fatal("handler shares the agent session")
	}

	os.Remove(marker)
	if _, err := Open("https://example.com/x", true, os.Environ()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, marker, "--url")

	if _, err := Open("ftp-like:thing", true, os.Environ()); err == nil || !strings.Contains(err.Error(), "no application") {
		t.Fatalf("unhandled scheme err = %v", err)
	}
	if _, err := Open(filepath.Join(root, "missing.txt"), false, os.Environ()); !os.IsNotExist(err) {
		t.Fatalf("missing file err = %v", err)
	}
}

func waitFor(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && string(data) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("%s = %q, want %q", path, data, want)
}

func mySid() int { sid, _ := unix.Getsid(0); return sid }
