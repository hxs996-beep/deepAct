//go:build windows

package ui

import (
	"syscall"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	getAsyncKeyState = user32.NewProc("GetAsyncKeyState")
)

const (
	vkMenu    = 0x12 // VK_MENU — the Alt key
	vkControl = 0x11 // VK_CONTROL — the Ctrl key
)

// optionKeyPressed returns true if the Alt key is currently held down.
// Uses GetAsyncKeyState to read the physical key state — no terminal
// cooperation required. The high bit (bit 15) indicates "key is down".
func optionKeyPressed() bool {
	ret, _, _ := getAsyncKeyState.Call(uintptr(vkMenu))
	return ret&0x8000 != 0
}

// ctrlKeyPressed returns true if the Ctrl key is currently held down.
func ctrlKeyPressed() bool {
	ret, _, _ := getAsyncKeyState.Call(uintptr(vkControl))
	return ret&0x8000 != 0
}
