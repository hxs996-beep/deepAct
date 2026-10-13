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

	// Simulate >100ms passing so the next event is not live (starts a new run).
	stallClock(ib)

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
	stallClock(ib)
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("456")})
	if len(ib.suffixSegments) != 2 || ib.suffixSegments[1].isPaste || ib.suffixSegments[1].text != "456" {
		t.Errorf("suffixSegments after typing 456 = %+v, want text segment", ib.suffixSegments)
	}

	// Second bracketed paste -> NEW paste segment with its own indicator.
	stallClock(ib)
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
	stallClock(ib)
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

	// First char arrives outside pasteGap -> text segment.
	stallClock(ib)
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

	// Prefix "x" (live, pre-run typing) seeds the run so the
	// following Enter events are all within pasteGap (live).
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
	stallClock(ib)
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("456")})
	stallClock(ib)
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
	stallClock(ib)
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("zzz")})
	stallClock(ib)
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
	stallClock(ib)

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

// TestWrapInputRowsPreservesANSI verifies that wrapInputRows never splits an
// ANSI escape sequence across a hard wrap. Long input lines containing the
// styled paste indicator must wrap between printable characters, not inside
// the escape sequence.
func TestWrapInputRowsPreservesANSI(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)

	ind := PasteIndicatorStyle.Render("[Pasted +4 lines]")
	// Width 13 forces the wrap point into the middle of the ANSI sequence
	// ("123 \x1b[38;5;240" accumulates exactly 13 before the final 'm').
	text := "123 " + ind + " 456"

	rows, starts := wrapInputRows(text, 13)
	if len(rows) == 0 || len(starts) != len(rows) {
		t.Fatalf("wrapInputRows: %d rows, %d starts", len(rows), len(starts))
	}
	for _, line := range rows {
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

// stallClock simulates an input event arriving more than pasteGap after the
// previous one: a delivery/render hiccup, not a change of input source.
func stallClock(ib *InputBuffer) {
	ib.burst.lastAt = time.Now().Add(-2 * pasteGap)
}

// feedWindowsPaste replays a Windows ConPTY paste: one KeyMsg per character,
// with Paste never set (the console reader bypasses Bubble Tea's ANSI parser).
func feedWindowsPaste(ib *InputBuffer, text string) {
	for _, r := range text {
		if r == '\n' {
			ib.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})
			continue
		}
		ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// TestTypedMultiLineNeverFoldsIntoPaste is the reported bug: typing more than
// three lines by hand (Shift/Alt+Enter for the newlines) and then continuing to
// type made the input show "[Pasted +1 lines]" — the just-typed character was
// mistaken for a one-line paste. No paste happened, so the input must stay in
// plain multi-line mode.
func TestTypedMultiLineNeverFoldsIntoPaste(t *testing.T) {
	ib := NewInputBuffer()

	// Four lines typed by hand; the newlines come from Shift/Alt+Enter, which
	// the model routes to HandleKey as Alt+Enter.
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("aaa")})
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bbb")})
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ccc")})
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	// The first character of the fourth line follows immediately — the fastest
	// timing a human can produce.
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})

	if ib.PasteMode {
		t.Errorf("typing four lines must not enter PasteMode, segments = %+v", ib.suffixSegments)
	}
	if got := ib.Value(); got != "aaa\nbbb\nccc\nd" {
		t.Errorf("Value = %q, want %q", got, "aaa\nbbb\nccc\nd")
	}
	if strings.Contains(ib.Value(), "[Pasted") {
		t.Errorf("Value = %q must not contain a paste indicator", ib.Value())
	}
	if got := ib.SubmitContent(); got != "aaa\nbbb\nccc\nd" {
		t.Errorf("SubmitContent = %q, want %q", got, "aaa\nbbb\nccc\nd")
	}
}

// TestDeliberateNewlineClosesBurstSoEnterSubmits verifies that a deliberate
// newline (Alt/Shift+Enter) closes the current input run: the Enter pressed
// right afterwards is the user submitting, not a pasted newline.
func TestDeliberateNewlineClosesBurstSoEnterSubmits(t *testing.T) {
	ib := NewInputBuffer()

	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})

	if got := ib.HandleKey(tea.KeyMsg{Type: tea.KeyEnter}); got != ActionSubmit {
		t.Errorf("Enter after a deliberate newline = %v, want ActionSubmit", got)
	}
}

// TestPasteFoldCountsNewlinesPerBurstNotPerBuffer verifies that the paste
// threshold only counts newlines inserted by the current run. Two separate
// three-line pastes must not add up to a fold, and the earlier paste's lines
// must not become a fake paste indicator.
func TestPasteFoldCountsNewlinesPerBurstNotPerBuffer(t *testing.T) {
	ib := NewInputBuffer()

	// First paste: three lines, two newlines — below the fold threshold.
	feedWindowsPaste(ib, "a\nb\nc")
	if ib.PasteMode {
		t.Fatalf("a three-line paste must not fold, Value = %q", ib.Value())
	}

	// The user pastes a second three-line block after a pause.
	stallClock(ib)
	feedWindowsPaste(ib, "d\ne\nf")

	if ib.PasteMode {
		t.Errorf("two three-line pastes must not fold, Value = %q, segments = %+v", ib.Value(), ib.suffixSegments)
	}
	if got, want := ib.Value(), "a\nb\ncd\ne\nf"; got != want {
		t.Errorf("Value = %q, want %q", got, want)
	}
}

// TestWindowsLongSingleLinePasteFolds verifies that a long single-line paste
// folds on Windows too (where every character arrives as its own KeyMsg and
// msg.Paste is never set): the rune threshold applies to the run, matching the
// bracketed-paste behaviour on macOS.
func TestWindowsLongSingleLinePasteFolds(t *testing.T) {
	ib := NewInputBuffer()

	line := strings.Repeat("x", pasteThresholdRunes+50)
	stallClock(ib) // the paste starts after a pause
	feedWindowsPaste(ib, line)

	if !ib.PasteMode {
		t.Fatalf("a %d-rune single-line paste must fold, Value = %q", len(line), ib.Value())
	}
	if got := ib.Value(); !strings.Contains(got, "[Pasted +1 lines]") {
		t.Errorf("Value = %q, want a single-line paste indicator", got)
	}
	if got := ib.SubmitContent(); got != line {
		t.Errorf("SubmitContent length = %d, want %d", len(got), len(line))
	}
}

// TestStalledPasteKeepsSingleIndicator verifies that one hiccup (an event
// arriving more than pasteGap after the previous one) during a paste that is
// still streaming does not split it: the content stays inside the same
// indicator instead of spilling into the input as raw text.
func TestStalledPasteKeepsSingleIndicator(t *testing.T) {
	ib := NewInputBuffer()

	stallClock(ib)
	feedWindowsPaste(ib, "l1\nl2\nl3\nl4")
	if !ib.PasteMode {
		t.Fatal("expected PasteMode after a four-line paste")
	}
	if len(ib.suffixSegments) != 1 || !ib.suffixSegments[0].isPaste {
		t.Fatalf("segments = %+v, want one paste segment", ib.suffixSegments)
	}

	stallClock(ib) // hiccup mid-paste
	ib.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Z")})

	if len(ib.suffixSegments) != 1 || !ib.suffixSegments[0].isPaste {
		t.Errorf("segments = %+v, want the pasted content to stay in one segment", ib.suffixSegments)
	}
	if strings.Contains(ib.Value(), "Z") {
		t.Errorf("Value = %q must not expose pasted content as raw text", ib.Value())
	}
	if got := ib.PasteContent; got != "l1\nl2\nl3\nl4Z" {
		t.Errorf("PasteContent = %q, want %q", got, "l1\nl2\nl3\nl4Z")
	}
}
