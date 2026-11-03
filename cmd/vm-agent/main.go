// Command vm-agent is the in-VM desktop automation service described by
// docs/spec.md.
package main

import (
	"fmt"
	"os"

	"github.com/CIPFZ/go-agent-platform/internal/config"
)

func main() {
	cfg, err := config.Parse(os.Args[1:], os.Stderr)
	if err != nil {
		os.Exit(2)
	}
	_ = cfg
	fmt.Fprintln(os.Stderr, "vm-agent: HTTP server not implemented yet")
	os.Exit(1)
}
