//go:build !windows

package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

func desktopSessionAvailable() bool {
	return strings.TrimSpace(os.Getenv("DISPLAY")) != "" || strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) != ""
}

func captureDesktopScreenshot(ctx context.Context, path, commandName string) error {
	if commandName == "" {
		commandName = "gnome-screenshot"
	}
	return exec.CommandContext(ctx, commandName, "-f", path).Run()
}
