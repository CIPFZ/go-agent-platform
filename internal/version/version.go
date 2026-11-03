// Package version holds the vm-agent service version (semantic versioning,
// independent of the /v1 API version). Release builds set it with
// -ldflags "-X github.com/CIPFZ/go-agent-platform/internal/version.Version=1.2.3".
package version

// Version is the service version reported in X-Agent-Version and /v1/probe.
var Version = "0.1.0-dev"
