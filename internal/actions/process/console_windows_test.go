//go:build windows

package process

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// Console children of the windowless vm-agent.exe must not open console
// windows on the desktop, and stay suspended until they are in the Job.
func TestTreeHidesConsole(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit 0")
	tree, err := StartTree(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Wait(); err != nil {
		t.Fatal(err)
	}
	const want = windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED
	if f := cmd.SysProcAttr.CreationFlags; f&want != want {
		t.Fatalf("creation flags %#x lack %#x", f, want)
	}
}
