//go:build !linux

package process

import "errors"

// Job Objects make the registry unnecessary on Windows.
const trackTrees = false

func startTime(int) (string, error) { return "", errors.New("not supported") }

func killGroup(int) error { return errors.New("not supported") }
