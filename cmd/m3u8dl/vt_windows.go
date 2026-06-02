//go:build windows
// +build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

func init() {
	enableWindowsVT()
}

func enableWindowsVT() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode := kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode := kernel32.NewProc("SetConsoleMode")

	var mode uint32
	handle := syscall.Handle(os.Stderr.Fd())
	r, _, _ := procGetConsoleMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&mode)))
	if r != 0 {
		// ENABLE_VIRTUAL_TERMINAL_PROCESSING = 0x0004
		procSetConsoleMode.Call(uintptr(handle), uintptr(mode|0x0004))
	}
}
