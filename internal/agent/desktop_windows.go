//go:build windows

package agent

import (
	"context"
	"os"
	"os/exec"
)

// The Windows agent runs in the logged-in desktop session. PowerShell and
// System.Drawing provide a built-in capture path without an external helper.
func desktopSessionAvailable() bool { return true }

func captureDesktopScreenshot(ctx context.Context, path, _ string) error {
	const script = `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$screen = [System.Windows.Forms.Screen]::PrimaryScreen
if ($null -eq $screen) { throw 'no primary screen' }
$bounds = $screen.Bounds
$bitmap = New-Object System.Drawing.Bitmap -ArgumentList $bounds.Width,$bounds.Height
$graphics = [System.Drawing.Graphics]::FromImage($bitmap)
$graphics.CopyFromScreen($bounds.Location, [System.Drawing.Point]::Empty, $bounds.Size)
$bitmap.Save($env:GO_AGENT_SCREENSHOT_PATH, [System.Drawing.Imaging.ImageFormat]::Png)
$graphics.Dispose()
$bitmap.Dispose()`

	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	command.Env = append(os.Environ(), "GO_AGENT_SCREENSHOT_PATH="+path)
	return command.Run()
}
