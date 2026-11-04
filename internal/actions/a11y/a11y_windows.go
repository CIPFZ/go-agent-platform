//go:build windows

package a11y

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"unicode/utf16"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

//go:embed uia.ps1
var uiaScript string

var platform collector = uia{}

type uia struct{}

// command runs the embedded UI Automation script with Windows PowerShell,
// passed as -EncodedCommand so no file is written.
func (uia) command(req Request) (*exec.Cmd, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	units := utf16.Encode([]rune(uiaScript))
	raw := make([]byte, len(units)*2)
	for i, u := range units {
		raw[i*2], raw[i*2+1] = byte(u), byte(u>>8)
	}
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", base64.StdEncoding.EncodeToString(raw))
	cmd.Env = append(os.Environ(), helperEnv+"="+string(data))
	return cmd, nil
}

// Available reports whether accessibility trees can be collected; UI
// Automation ships with every supported Windows version.
func Available() bool {
	_, err := os.Stat(filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
	return err == nil
}

// walk is not used on Windows: the PowerShell script is the helper.
func walk(Request) (*Result, error) {
	return nil, protocol.Errorf(protocol.CodeInternal, "the Windows accessibility helper is the PowerShell script")
}
