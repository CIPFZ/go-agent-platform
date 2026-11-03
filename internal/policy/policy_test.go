package policy

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeList(t *testing.T, lines string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "list")
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDisabledByDefault(t *testing.T) {
	p, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled() || p.Check("anything") != nil {
		t.Fatal("empty policy must allow everything")
	}
	var nilPolicy *Policy
	if nilPolicy.Check("x") != nil {
		t.Fatal("nil policy must allow everything")
	}
}

func TestDenylistBeforeAllowlist(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	allow := writeList(t, "# comment\nsh\nprintf\n")
	deny := writeList(t, sh+"\n")
	p, err := Load(allow, deny)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Check("sh"); !errors.Is(err, ErrDenied) {
		t.Fatalf("sh resolved to a denied absolute path: err = %v", err)
	}
	if err := p.Check("printf"); err != nil {
		t.Fatalf("printf allowed: %v", err)
	}
	if err := p.Check("ls"); !errors.Is(err, ErrDenied) {
		t.Fatalf("ls not allowlisted: err = %v", err)
	}
}

func TestDenyByNameMatchesPathForms(t *testing.T) {
	p, err := Load("", writeList(t, "rm\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, argv0 := range []string{"rm", "/bin/rm", "/usr/bin/../bin/rm", "/somewhere/else/rm"} {
		if err := p.Check(argv0); !errors.Is(err, ErrDenied) {
			t.Errorf("Check(%q) = %v, want denied", argv0, err)
		}
	}
	if err := p.Check("ls"); err != nil {
		t.Errorf("ls must pass a denylist-only policy: %v", err)
	}
}

func TestRejectsRelativePathEntries(t *testing.T) {
	if _, err := Load(writeList(t, "bin/rm\n"), ""); err == nil {
		t.Fatal("relative path entry accepted")
	}
}
