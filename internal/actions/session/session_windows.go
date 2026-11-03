//go:build windows

package session

func detect() State { return State{Type: "console"} }

func initDPIAwareness() string { return "unaware" }
