//go:build !linux && !windows

package main

func procStats(int) (int64, int, int) { return 0, 0, 0 }
