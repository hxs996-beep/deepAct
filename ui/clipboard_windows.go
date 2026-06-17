//go:build windows

package ui

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	modkernel32          = syscall.NewLazyDLL("kernel32.dll")
	moduser32            = syscall.NewLazyDLL("user32.dll")
	procOpenClipboard    = moduser32.NewProc("OpenClipboard")
	procCloseClipboard   = moduser32.NewProc("CloseClipboard")
	procEmptyClipboard   = moduser32.NewProc("EmptyClipboard")
	procSetClipboardData = moduser32.NewProc("SetClipboardData")
	procGlobalAlloc      = modkernel32.NewProc("GlobalAlloc")
	procGlobalLock       = modkernel32.NewProc("GlobalLock")
	procGlobalUnlock     = modkernel32.NewProc("GlobalUnlock")
)

const (
	gmemMoveable  = 0x0002
	cfUnicodeText = 13
)

func windowsCopyImpl(utf16Data []uint16) error {
	r, _, _ := procOpenClipboard.Call(0)
	if r == 0 {
		return fmt.Errorf("OpenClipboard failed")
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()

	size := len(utf16Data)*2 + 2 // bytes + null terminator
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(size))
	if h == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return fmt.Errorf("GlobalLock failed")
	}

	dst := unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(utf16Data)+1)
	copy(dst, utf16Data)
	dst[len(utf16Data)] = 0
	procGlobalUnlock.Call(h)

	r, _, _ = procSetClipboardData.Call(cfUnicodeText, h)
	if r == 0 {
		return fmt.Errorf("SetClipboardData failed")
	}
	return nil
}

func utf16EncodeString(s string) ([]uint16, error) {
	return utf16.Encode([]rune(s)), nil
}
