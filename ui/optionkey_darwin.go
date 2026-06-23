//go:build darwin

package ui

import "github.com/ebitengine/purego"

// CoreGraphics / CGEvent constants (see CoreGraphics/CGEventSource.h and
// Carbon/HIToolbox/Events.h). Hard-coded so we need no C headers.
const (
	// kCGEventSourceStateCombinedSessionState — the event source state that
	// reflects hardware events from all sessions.
	cgEventSourceStateCombinedSessionState = 0
	// kCGEventFlagMaskAlternate — bit set in the modifier-flag word when the
	// Option (⌥) key is held.
	cgEventFlagMaskAlternate = 0x00080000
)

// cgEventSourceFlagsState is bound to the macOS CoreGraphics symbol
// CGEventSourceFlagsState at init() via purego. nil means the framework could
// not be loaded (e.g. a hardened sandbox); optionKeyPressed then reports false
// and the app falls back to bubbletea's standard ESC-prefix Alt detection.
var cgEventSourceFlagsState func(uint32) uint64

func init() {
	lib, err := purego.Dlopen(
		"/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics",
		purego.RTLD_LAZY|purego.RTLD_GLOBAL,
	)
	if err != nil {
		return
	}
	// RegisterLibFunc panics if the symbol is missing; recover so a failed
	// binding degrades gracefully instead of crashing the program.
	defer func() { _ = recover() }()
	var fn func(uint32) uint64
	purego.RegisterLibFunc(&fn, lib, "CGEventSourceFlagsState")
	cgEventSourceFlagsState = fn
}

// optionKeyPressed returns true if the Option (⌥) key is currently held.
//
// Implemented with the macOS CoreGraphics HID API (CGEventSourceFlagsState)
// through purego — no cgo, no C toolchain, and no system-header dependencies,
// so it builds cleanly with CGO_ENABLED=0 and imposes no setup on users
// building from source. This is required because iTerm2 and Terminal.app in
// their default Option=Normal mode send a plain \r for Option+Enter (byte-
// identical to Enter), so bubbletea's msg.Alt — which relies on an ESC prefix
// — cannot distinguish the two. Reading the physical modifier state closes
// that gap with zero terminal configuration.
func optionKeyPressed() bool {
	if cgEventSourceFlagsState == nil {
		return false
	}
	return cgEventSourceFlagsState(cgEventSourceStateCombinedSessionState)&cgEventFlagMaskAlternate != 0
}

// ctrlKeyPressed is unused on macOS but must exist so the platform-agnostic
// stub (optionkey_stub.go) is excluded when this file is compiled.
func ctrlKeyPressed() bool { return false }
