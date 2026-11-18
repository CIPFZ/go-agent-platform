//go:build !windows

package securedesk

import "github.com/CIPFZ/go-agent-platform/internal/actions/remote"

// NewClient is nil where there is no secure desktop helper.
func NewClient() remote.SecureDesktop { return nil }

// RunService and RunHelper exist on Windows only.
func RunService(string) int { return 2 }

// RunHelper exists on Windows only.
func RunHelper(string) int { return 2 }
