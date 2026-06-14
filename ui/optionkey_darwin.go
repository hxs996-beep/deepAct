//go:build darwin

package ui

/*
#cgo LDFLAGS: -framework ApplicationServices
#include <ApplicationServices/ApplicationServices.h>
#include <stdbool.h>

static bool _optionKeyDown(void) {
	CGEventFlags flags = CGEventSourceFlagsState(kCGEventSourceStateCombinedSessionState);
	if (flags == 0) return false;
	return (flags & kCGEventFlagMaskAlternate) != 0;
}
*/
import "C"

// optionKeyPressed returns true if the Option (⌥) key is currently held.
// Uses the macOS HID API (CGEventSourceFlagsState) — reads system state,
// no accessibility permissions required.
func optionKeyPressed() bool {
	return bool(C._optionKeyDown())
}
