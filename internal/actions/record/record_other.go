//go:build !linux && !windows

package record

import "github.com/CIPFZ/go-agent-platform/internal/protocol"

const grabberName = "unsupported"

func grabArgs(int, protocol.Rect) ([]string, error) {
	return nil, protocol.Unsupported("screen recording is not supported on this platform")
}

func ffmpegEnv(env []string) []string { return env }
