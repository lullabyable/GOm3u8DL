//go:build windows
// +build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procSetConsoleCursorPosition   = kernel32.NewProc("SetConsoleCursorPosition")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procFillConsoleOutputCharacter = kernel32.NewProc("FillConsoleOutputCharacterW")
	windowsVTEnabled               bool
)

type coord struct {
	X, Y int16
}

type smallRect struct {
	Left, Top, Right, Bottom int16
}

type consoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}

func init() {
	enableWindowsVT()
}

func enableWindowsVT() {
	handle := syscall.Handle(os.Stderr.Fd())
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&mode)))
	if r != 0 {
		// ENABLE_VIRTUAL_TERMINAL_PROCESSING = 0x0004
		r2, _, _ := procSetConsoleMode.Call(uintptr(handle), uintptr(mode|0x0004))
		windowsVTEnabled = r2 != 0
	}
}

// clearLine clears the current line using Windows console API.
func clearLine() {
	handle := syscall.Handle(os.Stderr.Fd())
	var info consoleScreenBufferInfo
	r, _, _ := procGetConsoleScreenBufferInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return
	}
	// Move cursor to start of current line
	startPos := coord{X: 0, Y: info.CursorPosition.Y}
	procSetConsoleCursorPosition.Call(uintptr(handle), uintptr(*(*int32)(unsafe.Pointer(&startPos))))
	// Fill the line with spaces
	var written uint32
	lineWidth := uintptr(info.Size.X)
	procFillConsoleOutputCharacter.Call(uintptr(handle), uintptr(' '), lineWidth, uintptr(*(*int32)(unsafe.Pointer(&startPos))), uintptr(unsafe.Pointer(&written)))
	// Move cursor back to start
	procSetConsoleCursorPosition.Call(uintptr(handle), uintptr(*(*int32)(unsafe.Pointer(&startPos))))
}

func terminalWidth() int {
	handle := syscall.Handle(os.Stderr.Fd())
	var info consoleScreenBufferInfo
	r, _, _ := procGetConsoleScreenBufferInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&info)))
	if r == 0 || info.Size.X <= 0 {
		return 80
	}
	return int(info.Size.X)
}

func supportsANSIColor() bool {
	return windowsVTEnabled
}
