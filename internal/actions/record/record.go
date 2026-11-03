// Package record implements /v1/record: one ffmpeg screen recording at a
// time, stored under -record-dir with quota and retention cleanup.
package record

import "github.com/CIPFZ/go-agent-platform/internal/protocol"

// grabArgs returns the ffmpeg input arguments that capture r at fps.
func grabArgs(fps int, r protocol.Rect) []string { return platformGrabArgs(fps, r) }
