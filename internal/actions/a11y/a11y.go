// Package a11y implements /v1/a11y/tree (phase 3): the accessibility tree,
// collected in a time-limited child process.
package a11y

import "github.com/CIPFZ/go-agent-platform/internal/protocol"

// Node is one accessibility element.
type Node struct {
	Role     string         `json:"role"`
	Name     string         `json:"name"`
	Value    string         `json:"value,omitempty"`
	Rect     *protocol.Rect `json:"rect"`
	State    []string       `json:"state"`
	Children []*Node        `json:"children"`
}
