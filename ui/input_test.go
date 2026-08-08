package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestPasteModeSecondBracketedPaste verifies that a second bracketed paste
// arriving while already in PasteMode (after >pasteGap) becomes a NEW paste
// segment with its own indicator, rather than being absorbed into the first.
func TestPasteModeSecondBracketedPaste(t *testing.T) {
	ib := NewInputBuffer()

	// First bracketed paste: 4 lines (3 newlines) -> triggers PasteMode.
	firstPaste := "line1\nline2\nline3\nline4"
	ib.HandleKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune(firstPaste),
		Paste: true,
	})

	if !ib.PasteMode {
		t.Fatal("expected PasteMode after first bracketed paste")
	}
	if len(ib.suffixSegments) != 1 {
		t.Fatalf("expected 1 suffix segment after first paste, got %+v", ib.suffixSegments)
	}

	// Simulate >100ms passing so the next event is !fast (slow).
	ib.lastEventTime = time.Now().Add(-200 * time.Millisecond)

	// Second bracketed paste: 3 more lines.
	secondPaste := "line5\nline6\nline7"
	ib.HandleKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune(secondPaste),
		Paste: true,
	})

	// Second paste must be its own paste segment (own indicator).
	if len(ib.suffixSegments) != 2 {
		t.Fatalf("suffixSegments = %+v, want 2 segments", ib.suffixSegments)
	}
	if !ib.suffixSegments[1].isPaste || ib.suffixSegments[1].text != secondPaste {
		t.Errorf("suffixSegments[1] = %+v, want paste segment %q", ib.suffixSegments[1], secondPaste)
	}

	// PasteContent should contain both pastes.
	expected := firstPaste + secondPaste
	if ib.PasteContent != expected {
		t.Errorf("PasteContent = %q, want %q", ib.PasteContent, expected)
	}
}

// TestPasteModeMultipleSegments verifies the reported scenario: typing 123,
// pasting A, typing 456, pasting B, typing 789 — each paste keeps its own
// indicator and everything is concatenated on submit.
func TestPasteModeMultipleSegments(t *testing.T) {
	ib := NewInputBuffer()

	// Type prefix "123" (slow, non-paste).
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("123")})

	// First bracketed paste: 4 lines -> triggers PasteMode.
	pasteA := "AAA\nBBB\nCCC\nDDD"
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasteA), Paste: true})

	if !ib.PasteMode {
		t.Fatal("expected PasteMode after first bracketed paste")
	}
	if ib.pastePrefix != "123" {
		t.Errorf("pastePrefix = %q, want %q", ib.pastePrefix, "123")
	}
	if len(ib.suffixSegments) != 1 || !ib.suffixSegments[0].isPaste || ib.suffixSegments[0].text != pasteA {
		t.Errorf("suffixSegments = %+v, want single paste segment %q", ib.suffixSegments, pasteA)
	}

	// User types "456" slowly -> goes to a text segment.
	ib.lastEventTime = time.Now().Add(-200 * time.Millisecond)
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("456")})
	if len(ib.suffixSegments) != 2 || ib.suffixSegments[1].isPaste || ib.suffixSegments[1].text != "456" {
		t.Errorf("suffixSegments after typing 456 = %+v, want text segment", ib.suffixSegments)
	}

	// Second bracketed paste -> NEW paste segment with its own indicator.
	ib.lastEventTime = time.Now().Add(-200 * time.Millisecond)
	pasteB := "EEE\nFFF\nGGG"
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasteB), Paste: true})

	if len(ib.suffixSegments) != 3 {
		t.Fatalf("suffixSegments after second paste = %+v, want 3 segments", ib.suffixSegments)
	}
	if !ib.suffixSegments[2].isPaste || ib.suffixSegments[2].text != pasteB {
		t.Errorf("suffixSegments[2] = %+v, want paste segment %q", ib.suffixSegments[2], pasteB)
	}

	// Display shows both indicators.
	if got := strings.Count(ib.Value(), "[Pasted +"); got != 2 {
		t.Errorf("visible text %q should contain 2 paste indicators, got %d", ib.Value(), got)
	}

	// User types "789" -> merges into trailing text segment.
	ib.lastEventTime = time.Now().Add(-200 * time.Millisecond)
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("789")})
	if got := strings.Count(ib.Value(), "[Pasted +"); got != 2 {
		t.Errorf("visible text %q should contain 2 paste indicators, got %d", ib.Value(), got)
	}

	// Submit concatenates prefix + pasteA + 456 + pasteB + 789.
	expected := "123" + pasteA + "456" + pasteB + "789"
	if got := ib.SubmitContent(); got != expected {
		t.Errorf("SubmitContent = %q, want %q", got, expected)
	}
}

// TestPasteModeFastTypingAfterBracketedPaste verifies that fast typing
// (each key within pasteGap) after a bracketed paste does NOT create a new
// "[Pasted +1 lines]" indicator. A bracketed paste arrives complete in one
// event; a fast KeyRunes event afterwards is just fast typing, not more paste.
func TestPasteModeFastTypingAfterBracketedPaste(t *testing.T) {
	ib := NewInputBuffer()

	// Bracketed paste -> PasteMode (complete paste arrived in one event).
	ib.HandleKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("line1\nline2\nline3\nline4"),
		Paste: true,
	})
	if !ib.PasteMode {
		t.Fatal("expected PasteMode after bracketed paste")
	}

	// First char arrives slow (>pasteGap after the paste) -> text segment.
	ib.lastEventTime = time.Now().Add(-200 * time.Millisecond)
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})

	// Second char arrives fast (<100ms after the first) — regression: this
	// was misclassified as a paste chunk and rendered "[Pasted +1 lines]".
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})

	if len(ib.suffixSegments) != 2 {
		t.Fatalf("suffixSegments = %+v, want 2 segments (paste + one text)", ib.suffixSegments)
	}
	last := ib.suffixSegments[1]
	if last.isPaste {
		t.Errorf("fast typing after bracketed paste must be a text segment, got %+v", last)
	}
	if last.text != "hi" {
		t.Errorf("last segment = %q, want %q", last.text, "hi")
	}
	if got := strings.Count(ib.Value(), "[Pasted +"); got != 1 {
		t.Errorf("visible text %q should contain exactly 1 paste indicator, got %d", ib.Value(), got)
	}

	// Submit must still return paste + typed text.
	expected := "line1\nline2\nline3\nline4" + "hi"
	if got := ib.SubmitContent(); got != expected {
		t.Errorf("SubmitContent = %q, want %q", got, expected)
	}
}

// TestPasteModeWindowsBurstContinuesPasteSegment verifies that a paste burst
// detected via fast events (Windows ConPTY, no bracketed paste markers) keeps
// absorbing subsequent fast events into the current paste segment — no new
// indicator is created mid-burst.
func TestPasteModeWindowsBurstContinuesPasteSegment(t *testing.T) {
	ib := NewInputBuffer()

	// Prefix "x" (slow, pre-burst typing) seeds lastEventTime so the
	// following Enter events are all within pasteGap (fast).
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if ib.PasteMode {
		t.Fatal("single slow rune must not enter PasteMode")
	}

	// Fast burst: consecutive KeyEnter events (all within pasteGap) carrying
	// newlines. After the 3rd newline the buffer enters PasteMode.
	for i := 0; i < 3; i++ {
		ib.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})
	}
	if !ib.PasteMode {
		t.Fatal("expected PasteMode after 3 fast newlines")
	}
	if len(ib.suffixSegments) != 1 || !ib.suffixSegments[0].isPaste {
		t.Fatalf("expected single paste segment, got %+v", ib.suffixSegments)
	}

	// More fast runes mid-burst -> absorbed into the existing paste segment.
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tail")})

	if len(ib.suffixSegments) != 1 {
		t.Fatalf("fast burst input must stay in one paste segment, got %+v", ib.suffixSegments)
	}
	if got := strings.Count(ib.Value(), "[Pasted +"); got != 1 {
		t.Errorf("visible text %q should contain exactly 1 paste indicator, got %d", ib.Value(), got)
	}
}

// TestPasteModeBackspaceRemovesTrailingPasteSegment verifies that pressing
// Backspace when the input ends with a paste indicator removes only that
// paste segment — the prefix and earlier segments survive.
func TestPasteModeBackspaceRemovesTrailingPasteSegment(t *testing.T) {
	ib := NewInputBuffer()

	// Type prefix "123", paste A (4 lines), type "456", paste B (3 lines).
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("123")})
	pasteA := "AAA\nBBB\nCCC\nDDD"
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasteA), Paste: true})
	ib.lastEventTime = time.Now().Add(-200 * time.Millisecond)
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("456")})
	ib.lastEventTime = time.Now().Add(-200 * time.Millisecond)
	pasteB := "EEE\nFFF\nGGG"
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasteB), Paste: true})

	if len(ib.suffixSegments) != 3 {
		t.Fatalf("setup: suffixSegments = %+v, want 3", ib.suffixSegments)
	}

	// Backspace removes ONLY the trailing paste segment.
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyBackspace})

	if len(ib.suffixSegments) != 2 {
		t.Fatalf("suffixSegments after Backspace = %+v, want 2 (paste + text)", ib.suffixSegments)
	}
	last := ib.suffixSegments[1]
	if last.isPaste || last.text != "456" {
		t.Errorf("last segment = %+v, want text segment %q", last, "456")
	}
	if got := strings.Count(ib.Value(), "[Pasted +"); got != 1 {
		t.Errorf("visible text %q should contain 1 paste indicator, got %d", ib.Value(), got)
	}
	expected := "123" + pasteA + "456"
	if got := ib.PasteContent; got != expected {
		t.Errorf("PasteContent = %q, want %q", got, expected)
	}
}

// TestPasteModeDeleteRemovesTrailingPasteSegment verifies that Delete has the
// same behavior as Backspace in PasteMode: it removes only the trailing paste
// segment.
func TestPasteModeDeleteRemovesTrailingPasteSegment(t *testing.T) {
	ib := NewInputBuffer()

	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pre")})
	pasteA := "AAA\nBBB\nCCC\nDDD"
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasteA), Paste: true})
	ib.lastEventTime = time.Now().Add(-200 * time.Millisecond)
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("zzz")})
	ib.lastEventTime = time.Now().Add(-200 * time.Millisecond)
	pasteB := "EEE\nFFF\nGGG"
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasteB), Paste: true})

	if len(ib.suffixSegments) != 3 {
		t.Fatalf("setup: suffixSegments = %+v, want 3", ib.suffixSegments)
	}

	ib.HandleKey(tea.KeyMsg{Type: tea.KeyDelete})

	if len(ib.suffixSegments) != 2 {
		t.Fatalf("suffixSegments after Delete = %+v, want 2 (paste + text)", ib.suffixSegments)
	}
	last := ib.suffixSegments[1]
	if last.isPaste || last.text != "zzz" {
		t.Errorf("last segment = %+v, want text segment %q", last, "zzz")
	}
}

// TestPasteModeBackspaceLastPasteFallsBackToPrefix verifies that removing the
// last remaining paste segment returns the buffer to plain input containing
// only the prefix — earlier typed text is preserved, not wiped.
func TestPasteModeBackspaceLastPasteFallsBackToPrefix(t *testing.T) {
	ib := NewInputBuffer()

	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("123")})
	pasteA := "AAA\nBBB\nCCC\nDDD"
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasteA), Paste: true})
	if !ib.PasteMode {
		t.Fatal("expected PasteMode after paste")
	}

	ib.HandleKey(tea.KeyMsg{Type: tea.KeyBackspace})

	if ib.PasteMode {
		t.Fatalf("expected PasteMode to end after removing the only paste, suffixSegments=%+v", ib.suffixSegments)
	}
	if got := ib.Value(); got != "123" {
		t.Errorf("Value = %q, want %q", got, "123")
	}
	if got := ib.SubmitContent(); got != "123" {
		t.Errorf("SubmitContent = %q, want %q", got, "123")
	}
}

// TestPasteModeBackspaceOnlyPasteClearsInput verifies that removing the only
// paste segment when there is no prefix leaves an empty input.
func TestPasteModeBackspaceOnlyPasteClearsInput(t *testing.T) {
	ib := NewInputBuffer()
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("AAA\nBBB\nCCC\nDDD"), Paste: true})
	if !ib.PasteMode {
		t.Fatal("expected PasteMode after paste")
	}

	ib.HandleKey(tea.KeyMsg{Type: tea.KeyBackspace})

	if ib.PasteMode {
		t.Error("expected PasteMode to end")
	}
	if got := ib.Value(); got != "" {
		t.Errorf("Value = %q, want empty", got)
	}
	if got := ib.SubmitContent(); got != "" {
		t.Errorf("SubmitContent = %q, want empty", got)
	}
}

// TestPasteModeSlowUserTypingGoesToSuffix verifies that non-paste (user typing)
// input arriving slowly in PasteMode still goes to a text segment (regression guard).
func TestPasteModeSlowUserTypingGoesToSuffix(t *testing.T) {
	ib := NewInputBuffer()

	// First bracketed paste -> triggers PasteMode.
	ib.HandleKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("line1\nline2\nline3\nline4"),
		Paste: true,
	})

	// Simulate >100ms passing.
	ib.lastEventTime = time.Now().Add(-200 * time.Millisecond)

	// User types text (NOT a paste) - should go to a text segment.
	ib.HandleKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("hello"),
		Paste: false,
	})

	if len(ib.suffixSegments) != 2 {
		t.Fatalf("suffixSegments = %+v, want 2 segments", ib.suffixSegments)
	}
	last := ib.suffixSegments[1]
	if last.isPaste || last.text != "hello" {
		t.Errorf("last segment = %+v, want text segment %q", last, "hello")
	}
}

// TestPasteModeValueNoBareReset verifies that the visible text embedded in
// ib.text (via rebuild) does not contain a bare ANSI reset. A bare \x1b[0m
// inside the input line resets InputBlockStyle's background mid-line, so the
// input box loses its background from the paste indicator onwards.
func TestPasteModeValueNoBareReset(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)

	ib := NewInputBuffer()
	ib.HandleKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("line1\nline2\nline3\nline4"),
		Paste: true,
	})
	if !ib.PasteMode {
		t.Fatal("expected PasteMode after paste")
	}
	val := ib.Value()
	if !strings.Contains(val, "[Pasted +") {
		t.Fatalf("Value() should contain paste indicator, got %q", val)
	}
	if strings.Contains(val, "\x1b[0m") {
		t.Errorf("Value() must not contain a bare ANSI reset (it kills InputBlockStyle background): %q", val)
	}
}

// TestWrapInputTextPreservesANSI verifies that wrapInputText never splits an
// ANSI escape sequence across a hard wrap. Long input lines containing the
// styled paste indicator must wrap between printable characters, not inside
// the escape sequence.
func TestWrapInputTextPreservesANSI(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)

	ind := PasteIndicatorStyle.Render("[Pasted +4 lines]")
	// Width 13 forces the wrap point into the middle of the ANSI sequence
	// ("123 \x1b[38;5;240" accumulates exactly 13 before the final 'm').
	text := "123 " + ind + " 456"

	wrapped := wrapInputText(text, 13)
	for _, line := range strings.Split(wrapped, "\n") {
		assertNoSplitANSISequence(t, line)
	}
}

// assertNoSplitANSISequence fails when a line contains an ESC '[' sequence
// that is cut off before its terminating 'm' (either by the line ending or
// by being continued on a different line).
func assertNoSplitANSISequence(t *testing.T, s string) {
	t.Helper()
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j == len(s) {
				t.Fatalf("ANSI sequence unterminated at offset %d in %q", i, s)
			}
		}
	}
}
