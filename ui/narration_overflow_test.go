package ui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestRenderMessage_NarrationFitsWidth verifies that narration message lines
// (after NarrationStyle padding) do not exceed the given width. NarrationStyle
// adds PaddingLeft(2) + PaddingRight(1) = 3 columns on top of renderMarkdown's
// output. If renderMarkdown uses the full width, the padded lines overflow the
// terminal, causing auto-wrap that corrupts the diff renderer's line tracking
// and produces garbled text.
func TestRenderMessage_NarrationFitsWidth(t *testing.T) {
	width := 40
	msg := DisplayMessage{
		Role: "narration",
		Content: "任务已完成。总结：\n" +
			"1. 已修改4个文件，让 deepact skill 系统支持 Markdown 格式\n" +
			"2. 安装 super-powers skill",
	}
	lines := renderMessage(msg, width)
	if len(lines) == 0 {
		t.Fatal("renderMessage returned no lines for narration")
	}
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w > width {
			t.Errorf("line %d visual width=%d exceeds max %d: %q",
				i, w, width, stripAnsi(line))
		}
	}
}

// TestFlushNarration_MovesPendingToNarration verifies that flushNarration
// transfers buffered content_delta text from narrationPending to narration,
// then clears the pending buffer.
func TestFlushNarration_MovesPendingToNarration(t *testing.T) {
	m := &Model{
		narration:        "existing",
		narrationPending: " appended",
	}
	m.flushNarration()
	if m.narration != "existing appended" {
		t.Errorf("narration = %q, want %q", m.narration, "existing appended")
	}
	if m.narrationPending != "" {
		t.Errorf("narrationPending should be empty after flush, got %q", m.narrationPending)
	}
}

// TestFlushNarration_NoopOnEmpty verifies that flushNarration does nothing
// when narrationPending is empty.
func TestFlushNarration_NoopOnEmpty(t *testing.T) {
	m := &Model{
		narration: "keep me",
	}
	m.flushNarration()
	if m.narration != "keep me" {
		t.Errorf("narration = %q, want %q", m.narration, "keep me")
	}
}

// TestFinalizeTurnBlocks_FlushesPendingNarration verifies that
// finalizeTurnBlocks flushes narrationPending before snapshotting, so no
// buffered content_delta text is lost at turn boundaries.
func TestFinalizeTurnBlocks_FlushesPendingNarration(t *testing.T) {
	m := &Model{
		narration:        "base narration",
		narrationPending: " +pending",
		messages:         []DisplayMessage{},
	}
	m.finalizeTurnBlocks()
	if len(m.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(m.messages))
	}
	want := "base narration +pending"
	if m.messages[0].Content != want {
		t.Errorf("snapshot content = %q, want %q", m.messages[0].Content, want)
	}
}
