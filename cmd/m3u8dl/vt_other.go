//go:build !windows
// +build !windows

package main

func enableWindowsVT() {
	// No-op on non-Windows platforms
}

func clearLine() {
	// No-op on non-Windows platforms (ANSI codes used instead)
}
