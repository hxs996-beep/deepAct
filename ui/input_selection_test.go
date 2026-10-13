package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/deepact/deepact/engine"
)

// TestDragBelowBodyDoesNotClaimCopied pins the reported false feedback: a drag
// that lands outside the body (input box / status bar) selects no text, so the
// status bar must NOT claim "✓ Copied" while the clipboard was never written.
func TestDragBelowBodyDoesNotClaimCopied(t *testing.T) {
	m := newInputTestModel()
	m.inputBuf.SetValue("hello world")
	got := stubClipboard(t, &m)

	// The status bar's workdir row is the bottom row of the view, below the
	// input box.
	row := m.height - 1
	press := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: row}
	res, _ := m.Update(press)
	m2 := res.(Model)
	rel := tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 20, Y: row}
	res, _ = m2.Update(rel)
	m3 := res.(Model)

	if !m3.clipboardFeedback.IsZero() {
		t.Errorf("status-bar drag reported a copy: clipboardFeedback=%v (nothing was copied)", m3.clipboardFeedback)
	}
	if *got != "" {
		t.Errorf("status-bar drag wrote %q to the clipboard", *got)
	}
	if m3.selection.Done || m3.selection.Active {
		t.Errorf("status-bar drag opened a body selection: %+v", m3.selection.Start)
	}
}

// newInputTestModel returns a ready model sized like a real terminal.
func newInputTestModel() Model {
	m := NewModel(nil, engine.PricingConfig{})
	m.ready = true
	m.state = stateReady
	m.width = 100
	m.height = 40
	return m
}

// stubClipboard injects a capture stub into m so a test never touches the real
// clipboard, and returns the pointer holding the captured text. The stub lives
// on the model, so no state is shared between tests.
func stubClipboard(t *testing.T, m *Model) *string {
	t.Helper()
	var got string
	m.clipboardWrite = func(text string) error {
		got = text
		return nil
	}
	return &got
}

// dragInInputBox drives the real mouse handler: press, motion, release.
func dragInInputBox(t *testing.T, m Model, fromCol, toCol int) Model {
	t.Helper()
	top, _, ok := m.inputBoxRange()
	if !ok {
		t.Fatal("input box geometry unavailable")
	}
	row := top + 1 // first text row (row 0 is the separator)
	res, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: fromCol, Y: row})
	m = res.(Model)
	res, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonNone, X: toCol, Y: row})
	m = res.(Model)
	res, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: toCol, Y: row})
	return res.(Model)
}

// TestInputDragSelectsAndCopiesText is the reported gap: dragging over the input
// box text must highlight the dragged text and copy exactly it.
func TestInputDragSelectsAndCopiesText(t *testing.T) {
	m := newInputTestModel()
	m.inputBuf.SetValue("hello world")
	got := stubClipboard(t, &m)

	_, height, ok := m.inputBoxRange()
	if !ok {
		t.Fatal("input box geometry unavailable")
	}
	if height != 2 {
		t.Fatalf("single-line input: want box height 2 (separator + text row), got %d", height)
	}

	m2 := dragInInputBox(t, m, 5, 10)

	if *got != "hello" {
		t.Errorf("clipboard: want %q, got %q", "hello", *got)
	}
	if !m2.inputSel.Done {
		t.Errorf("input selection should be Done after release: %+v", m2.inputSel)
	}
	if !strings.Contains(renderInputLine(m2), "\x1b[7m") {
		t.Error("input line has no selection highlight (reverse video)")
	}
	if m2.selection.Active || m2.selection.Done {
		t.Errorf("input-box drag must not open a body selection: %+v", m2.selection)
	}
	if m2.clipboardFeedback.IsZero() {
		t.Error("a successful copy should be reported in the status bar")
	}
}

// TestInputDragNothingSelectedNoFeedback: a click without movement selects no
// text, so nothing is copied and no "✓ Copied" is claimed.
func TestInputDragNothingSelectedNoFeedback(t *testing.T) {
	m := newInputTestModel()
	m.inputBuf.SetValue("hello world")
	got := stubClipboard(t, &m)

	m2 := dragInInputBox(t, m, 7, 7)

	if *got != "" {
		t.Errorf("single click wrote %q to the clipboard", *got)
	}
	if m2.inputSel.Active || m2.inputSel.Done {
		t.Errorf("single click should clear the selection: %+v", m2.inputSel)
	}
	if !m2.clipboardFeedback.IsZero() {
		t.Errorf("single click reported a copy: %v", m2.clipboardFeedback)
	}
}

// TestInputRuneAtCompensatesCaret: the caret is drawn as one cell inside the
// text, so a column after it maps one rune later in the buffer than the caret
// position would suggest.
func TestInputRuneAtCompensatesCaret(t *testing.T) {
	m := newInputTestModel()
	m.inputBuf.SetValue("hello world")
	for i := 0; i < 6; i++ { // caret between "hello" and " world"
		res, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
		m = res.(Model)
	}
	if m.inputBuf.Cursor() != 5 {
		t.Fatalf("setup: want cursor 5, got %d", m.inputBuf.Cursor())
	}

	top, _, _ := m.inputBoxRange()
	row := top + 1

	if idx := m.inputRuneAt(row, 5); idx != 0 {
		t.Errorf("first text cell: want rune 0, got %d", idx)
	}
	// Column 11 draws the space that follows the caret; in the buffer that
	// space is rune 5 (the caret itself is not a buffer rune).
	if idx := m.inputRuneAt(row, 11); idx != 5 {
		t.Errorf("cell after the caret: want rune 5, got %d", idx)
	}
}

// TestInputDragSelectsWideRunes: CJK cells are two columns wide; a drag must
// land on the rune boundary the user sees.
func TestInputDragSelectsWideRunes(t *testing.T) {
	m := newInputTestModel()
	m.inputBuf.SetValue("你好世界")
	got := stubClipboard(t, &m)

	top, _, _ := m.inputBoxRange()
	if idx := m.inputRuneAt(top+1, 10); idx != 2 {
		t.Errorf("second cell of 世: want rune 2, got %d", idx)
	}

	dragInInputBox(t, m, 5, 9)
	if *got != "你好" {
		t.Errorf("clipboard: want %q, got %q", "你好", *got)
	}
}

// TestInputDragAcrossWrappedRows: a long input wraps; the box grows, and a drag
// spanning two rows selects the runes of both.
func TestInputDragAcrossWrappedRows(t *testing.T) {
	m := newInputTestModel()
	m.width = 40 // inner width 34
	m.inputBuf.SetValue(strings.Repeat("a", 40))
	got := stubClipboard(t, &m)

	top, height, ok := m.inputBoxRange()
	if !ok {
		t.Fatal("input box geometry unavailable")
	}
	if height != 3 {
		t.Fatalf("wrapped input: want box height 3 (separator + 2 rows), got %d", height)
	}

	// Row 1 starts at the first wrapped row, row 2 at the second: dragging from
	// the first cell to the first cell of the second row selects row 1.
	res, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: top + 1})
	m = res.(Model)
	res, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonNone, X: 5, Y: top + 2})
	m = res.(Model)
	res, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 5, Y: top + 2})
	m = res.(Model)

	if want := strings.Repeat("a", 34); *got != want {
		t.Errorf("clipboard: want %d a's, got %d (%q)", len(want), len(*got), *got)
	}
}

// TestInputDragCopyFailureIsReported: a clipboard that refuses the write must
// surface as an error in the status bar, not as a successful copy.
func TestInputDragCopyFailureIsReported(t *testing.T) {
	m := newInputTestModel()
	m.inputBuf.SetValue("hello world")
	m.clipboardWrite = func(string) error { return errors.New("boom") }

	m2 := dragInInputBox(t, m, 5, 10)

	if m2.clipboardError == "" {
		t.Error("a failed clipboard write was not recorded")
	}
	if m2.clipboardFeedback.IsZero() {
		t.Error("a failed clipboard write must be reported in the status bar")
	}
	if s := renderStatusBar(m2.status, m2.workDir, m2.scrollOffset, 0, m2.width, m2.clipboardFeedback, m2.clipboardError); !strings.Contains(s, "Copy failed") {
		t.Errorf("status bar does not show the failure: %q", s)
	}
}

// TestInputDragOnPasteIndicatorCopiesWhatIsDrawn pins the paste-shorthand
// choice: the box draws "[Pasted +N lines]" in place of the pasted content, and
// a drag copies what is drawn. The indicator's cells have no 1:1 mapping to the
// hidden runes, so "copy what you see" — the same rule the transcript follows —
// is the only well-defined answer.
func TestInputDragOnPasteIndicatorCopiesWhatIsDrawn(t *testing.T) {
	m := newInputTestModel()
	m.inputBuf.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("AAA\nBBB\nCCC\nDDD"), Paste: true})
	if !m.inputBuf.PasteMode {
		t.Fatal("setup: expected PasteMode after a bracketed paste")
	}
	got := stubClipboard(t, &m)

	// The indicator "[Pasted +4 lines]" is 17 cells wide and starts at the first
	// text cell (input column 5); the drag ends on its last cell.
	dragInInputBox(t, m, 5, 5+17)

	if want := "[Pasted +4 lines]"; *got != want {
		t.Errorf("clipboard = %q, want %q", *got, want)
	}
}

// TestInputDragOnMultiLineInputCopiesTheDraggedRow pins the rune space: a real
// '\n' is a rune of the buffer but never a rune of a drawn row (the row split
// consumes it), so a row's start offset has to count the newlines before it.
// Getting that wrong leaves the highlight correct — it subtracts the same
// offset — while the clipboard copies from the wrong place.
func TestInputDragOnMultiLineInputCopiesTheDraggedRow(t *testing.T) {
	const text = "ab\ncd\nef"
	cases := []struct {
		row     int // 1-based text row inside the box
		wantIdx int // buffer rune the row starts at
		want    string
	}{
		{1, 0, "ab"},
		{2, 3, "cd"},
		{3, 6, "ef"},
	}

	for _, tc := range cases {
		m := newInputTestModel()
		m.inputBuf.SetValue(text)
		got := stubClipboard(t, &m)

		top, height, ok := m.inputBoxRange()
		if !ok || height != 4 {
			t.Fatalf("row %d: want box height 4 (separator + 3 text rows), got height=%d ok=%v", tc.row, height, ok)
		}
		row := top + tc.row
		if idx := m.inputRuneAt(row, 5); idx != tc.wantIdx {
			t.Errorf("row %d starts at rune %d, want %d", tc.row, idx, tc.wantIdx)
		}

		// Columns 5.. are the drawn text; one cell per ASCII rune.
		endCol := 5 + len(tc.want)
		res, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: row})
		m = res.(Model)
		res, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonNone, X: endCol, Y: row})
		m = res.(Model)
		res, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: endCol, Y: row})
		m = res.(Model)

		if *got != tc.want {
			t.Errorf("row %d drag: clipboard = %q, want %q", tc.row, *got, tc.want)
		}
	}
}

// TestPressOnPopupBandDoesNotSelectBody: the popups (suggestions / options /
// resume picker) are drawn between the transcript and the input box. Rows there
// show no transcript text, so a press on one must not open a body selection —
// it copied lines the user never pointed at.
func TestPressOnPopupBandDoesNotSelectBody(t *testing.T) {
	m := newInputTestModel()
	m.messages = []DisplayMessage{{Role: "assistant", Content: strings.Repeat("正文行\n", 60)}}
	m.inputBuf.SetValue("/")
	m.showSuggestions = true
	m.suggestions = []Suggestion{{Command: "/help", Description: "help"}}

	bodyBottom := m.bodyBottom()
	top, _, ok := m.inputBoxRange()
	if !ok || top <= bodyBottom {
		t.Fatalf("expected a popup band above the input box: bodyBottom=%d top=%d ok=%v", bodyBottom, top, ok)
	}
	got := stubClipboard(t, &m)

	res, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: bodyBottom})
	m = res.(Model)
	res, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonNone, X: 20, Y: bodyBottom})
	m = res.(Model)
	res, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 20, Y: bodyBottom})
	m = res.(Model)

	if m.selection.Active || m.selection.Done {
		t.Errorf("a press on a popup row opened a body selection: Start=%+v End=%+v", m.selection.Start, m.selection.End)
	}
	if *got != "" {
		t.Errorf("a press on a popup row copied %q", *got)
	}
	if !m.clipboardFeedback.IsZero() {
		t.Errorf("a press on a popup row reported a copy: %v", m.clipboardFeedback)
	}
}

// TestBodyDragStillUsesBodySelection: presses inside the body keep the existing
// body selection behaviour.
func TestBodyDragStillUsesBodySelection(t *testing.T) {
	m := newInputTestModel()
	m.messages = []DisplayMessage{{Role: "user", Content: "hello world"}}

	res, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: 0})
	m2 := res.(Model)

	if !m2.selection.Active {
		t.Error("body press should open a body selection")
	}
	if m2.inputSel.Active {
		t.Errorf("body press must not open an input selection: %+v", m2.inputSel)
	}
}

// TestKeyClearsInputSelection: typing dismisses a finished selection, matching
// the body-selection behaviour.
func TestKeyClearsInputSelection(t *testing.T) {
	m := newInputTestModel()
	m.inputBuf.SetValue("hello world")
	stubClipboard(t, &m)

	m2 := dragInInputBox(t, m, 5, 10)
	if !m2.inputSel.Done {
		t.Fatalf("setup: selection not done: %+v", m2.inputSel)
	}

	res, _ := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m3 := res.(Model)
	if m3.inputSel.Done || m3.inputSel.Active {
		t.Errorf("key press should clear the input selection: %+v", m3.inputSel)
	}
}

// TestHighlightRuneSpan wraps exactly the requested runes in reverse video.
func TestHighlightRuneSpan(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		from, to int
		want     string
	}{
		{"middle", "abcd", 1, 3, "a\x1b[7mbc\x1b[27md"},
		{"to end", "ab", 1, 2, "a\x1b[7mb\x1b[27m"},
		{"from start", "ab", 0, 1, "\x1b[7ma\x1b[27mb"},
		{"empty span", "ab", 1, 1, "ab"},
		{"whole", "ab", 0, 2, "\x1b[7mab\x1b[27m"},
	}
	for _, tc := range cases {
		if got := highlightRuneSpan(tc.in, tc.from, tc.to); got != tc.want {
			t.Errorf("%s: want %q, got %q", tc.name, tc.want, got)
		}
	}
}
