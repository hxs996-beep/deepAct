package ui

import "testing"

func TestReverseHighlightLine_FullLine(t *testing.T) {
	line := "hello world"
	result := reverseHighlightLine(line, 0, -1)
	want := "\x1b[7mhello world\x1b[27m"
	if result != want {
		t.Errorf("full line: want %q, got %q", want, result)
	}
}

func TestReverseHighlightLine_PartialLine(t *testing.T) {
	line := "hello world"
	result := reverseHighlightLine(line, 2, 7)
	want := "he\x1b[7mllo w\x1b[27morld"
	if result != want {
		t.Errorf("partial: want %q, got %q", want, result)
	}
}

func TestReverseHighlightLine_WithANSI(t *testing.T) {
	line := "\x1b[31mhello\x1b[0m world"
	result := reverseHighlightLine(line, 0, -1)
	if !containsSeq(result, "\x1b[7m") || !containsSeq(result, "\x1b[27m") {
		t.Errorf("with ANSI: missing reverse markers in %q", result)
	}
}

func TestReverseHighlightLine_ColStartBeyondLine(t *testing.T) {
	line := "hi"
	result := reverseHighlightLine(line, 10, -1)
	if result != "hi" {
		t.Errorf("col beyond line: want %q, got %q", "hi", result)
	}
}

func TestReverseHighlightLine_EmptyLine(t *testing.T) {
	result := reverseHighlightLine("", 0, -1)
	if result != "" {
		t.Errorf("empty line: want empty, got %q", result)
	}
}

func TestStripAnsi(t *testing.T) {
	input := "\x1b[31mhello\x1b[0m \x1b[1mworld\x1b[0m"
	result := stripAnsi(input)
	if result != "hello world" {
		t.Errorf("stripAnsi: want %q, got %q", "hello world", result)
	}
}

func TestStripAnsi_NoAnsi(t *testing.T) {
	result := stripAnsi("plain text")
	if result != "plain text" {
		t.Errorf("stripAnsi no ansi: want %q, got %q", "plain text", result)
	}
}

func TestExtractSelectionText_NoSelection(t *testing.T) {
	m := Model{}
	text := m.extractSelectionText()
	if text != "" {
		t.Errorf("empty selection should return empty string, got %q", text)
	}
}

func TestExtractSelectionText_WithContent(t *testing.T) {
	m := Model{
		cachedBodyLines: []string{
			"first line",
			"second line",
			"third line",
		},
	}
	m.selection = SelectionState{
		Done:  true,
		Start: selPoint{Line: 0, Col: 0},
		End:   selPoint{Line: 2, Col: 5},
	}
	text := m.extractSelectionText()
	if text == "" {
		t.Error("expected non-empty extracted text")
	}
	if !containsSeq(text, "first line") {
		t.Errorf("expected 'first line' in extracted text, got %q", text)
	}
}
