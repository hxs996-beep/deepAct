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

// TestNarrationSnapshot_MatchesStreamingLineStructure guards the fix for
// UI 错行: when the AI quotes a read result (line-numbered code + the
// "──────────────" separator + lsp hint, all OUTSIDE a code fence), the
// finalized narration snapshot must have EXACTLY the same line structure and
// text as the streamed narration. Previously the snapshot rendered via
// glamour, which merged adjacent non-fenced lines into a paragraph and
// re-wrapped them, producing a different line count than renderStreaming —
// the line-count jump between streaming and finalize shifted scrollOffset /
// screenToLine mapping mid-view (scrolling showed wrong rows, clicking
// selected a neighbour's line).
func TestNarrationSnapshot_MatchesStreamingLineStructure(t *testing.T) {
	content := "analysis report and get user confirmation. This gate\n" +
		"  479: // fires when the agent has done searches (runToolCallCount > 0) and is now\n" +
		"  480: // attempting to modify code, but hasn't yet presented its findings to the\n" +
		"  481: // user. After 2 blocks, the gate gives up and lets edits proceed directly\n" +
		"  482: // in degraded mode (see degradation handler below).\n" +
		"  483: if e.runToolCallCount > 0 && !e.state.AnalysisReportConfirmed &&\n" +
		"  484: e.analysisNudgeCount < 2 {\n" +
		"  485: var editCalls []ToolCallRequest\n" +
		"  486: for _, call := range calls {\n" +
		"  487: if call.Name == \"edit\" || call.Name == \"write\" {\n" +
		"  488: editCalls = append(editCalls, call)\n" +
		"  489: }\n" +
		"\n" +
		"  ---\n" +
		"\n" +
		"  Need to find a symbol definition, type info, or references? Use the `lsp` tool."

	for _, width := range []int{117, 100, 80, 60, 50, 40} {
		stream := renderStreaming(content, width)
		snap := renderMessage(DisplayMessage{Role: "narration", Content: content}, width)

		if len(stream) != len(snap) {
			t.Fatalf("width=%d: streaming line count %d != snapshot line count %d",
				width, len(stream), len(snap))
		}
		for i := range stream {
			s := stripAnsi(stream[i])
			n := stripAnsi(snap[i])
			if s != n {
				t.Errorf("width=%d line %d text differs:\n  stream: %q\n  snap:   %q", width, i, s, n)
			}
		}
	}
}
