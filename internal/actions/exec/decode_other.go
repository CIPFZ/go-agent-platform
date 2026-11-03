//go:build !windows

package exec

func decodeCodePage([]byte) (string, bool) { return "", false }
