package ui

import (
	"strings"
	"testing"
)

func leadingSpaces(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}

// TestNarrationAlignment_MatchesAssistant guards the ROOT CAUSE of the
// long-standing text misalignment: narration was rendered with
// NarrationStyle(PaddingLeft 2) + glamour Document.Margin(2) = 4 leading
// columns, while assistant messages only get glamour's 2-column margin.
// Both must share identical column alignment so blocks line up in the body.
func TestNarrationAlignment_MatchesAssistant(t *testing.T) {
	content := "探索完成，已方案定（布局 B +机制 A）。进入现在设计展示，分节确认。"
	width := 80

	narrLines := renderMessage(DisplayMessage{Role: "narration", Content: content}, width)
	asstLines := renderMessage(DisplayMessage{Role: "assistant", Content: content}, width)

	if len(narrLines) == 0 || len(asstLines) == 0 {
		t.Fatal("expected non-empty render output")
	}
	nIndent := leadingSpaces(stripAnsi(narrLines[0]))
	aIndent := leadingSpaces(stripAnsi(asstLines[0]))
	if nIndent != aIndent {
		t.Errorf("narration indent=%d != assistant indent=%d", nIndent, aIndent)
	}
}

// TestNarrationAlignment_StreamMatchesSnapshot guards the second half of the
// misalignment: while streaming, narration was rendered flush-left (indent 0);
// after finalize it jumped to indent 4. Both must render with the same indent
// so text does not shift horizontally when the turn finalizes.
func TestNarrationAlignment_StreamMatchesSnapshot(t *testing.T) {
	content := "探索完成，已方案定（布局 B +机制 A）。进入现在设计展示，分节确认。"
	width := 80

	streamLines := renderStreaming(content, width)
	snapLines := renderMessage(DisplayMessage{Role: "narration", Content: content}, width)

	if len(streamLines) == 0 || len(snapLines) == 0 {
		t.Fatal("expected non-empty render output")
	}
	sIndent := leadingSpaces(stripAnsi(streamLines[0]))
	nIndent := leadingSpaces(stripAnsi(snapLines[0]))
	if sIndent != nIndent {
		t.Errorf("streaming indent=%d != snapshot indent=%d", sIndent, nIndent)
	}
}
