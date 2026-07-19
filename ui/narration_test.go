package ui

import (
	"testing"
)

// TestFinalizeTurnBlocks_NarrationAndToolSnapshot verifies that
// finalizeTurnBlocks snapshots both narration and toolTree into display
// messages, then clears both fields.
func TestFinalizeTurnBlocks_NarrationAndToolSnapshot(t *testing.T) {
	m := &Model{
		narration: "让我搜索渲染逻辑",
		toolTree: []ToolNode{
			{Name: "grep", Detail: "streaming", Done: true},
		},
		messages: []DisplayMessage{},
	}

	m.finalizeTurnBlocks()

	if len(m.messages) != 2 {
		t.Fatalf("expected 2 messages (narration + toolsummary), got %d", len(m.messages))
	}
	if m.messages[0].Role != "narration" {
		t.Errorf("first message role = %q, want narration", m.messages[0].Role)
	}
	if m.messages[0].Content != "让我搜索渲染逻辑" {
		t.Errorf("narration content = %q, want %q", m.messages[0].Content, "让我搜索渲染逻辑")
	}
	if m.messages[1].Role != "toolsummary" {
		t.Errorf("second message role = %q, want toolsummary", m.messages[1].Role)
	}
	if m.narration != "" {
		t.Errorf("narration should be cleared after finalize, got %q", m.narration)
	}
	if len(m.toolTree) != 0 {
		t.Errorf("toolTree should be cleared after finalize, got %d items", len(m.toolTree))
	}
}

// TestFinalizeTurnBlocks_EmptyNarrationSkipped verifies that when narration
// is empty, only the toolTree snapshot is produced (no empty narration message).
func TestFinalizeTurnBlocks_EmptyNarrationSkipped(t *testing.T) {
	m := &Model{
		narration: "",
		toolTree: []ToolNode{
			{Name: "grep", Detail: "test", Done: true},
		},
		messages: []DisplayMessage{},
	}

	m.finalizeTurnBlocks()

	if len(m.messages) != 1 {
		t.Fatalf("expected 1 message (toolsummary only), got %d", len(m.messages))
	}
	if m.messages[0].Role != "toolsummary" {
		t.Errorf("message role = %q, want toolsummary", m.messages[0].Role)
	}
}

// TestFinalizeTurnBlocks_WhitespaceOnlyNarrationSkipped verifies that
// whitespace-only narration does not produce a narration message.
func TestFinalizeTurnBlocks_WhitespaceOnlyNarrationSkipped(t *testing.T) {
	m := &Model{
		narration: "   \n\t  ",
		toolTree:  nil,
		messages:  []DisplayMessage{},
	}

	m.finalizeTurnBlocks()

	if len(m.messages) != 0 {
		t.Fatalf("expected 0 messages for whitespace-only narration, got %d: %+v", len(m.messages), m.messages)
	}
}

// TestFinalizeTurnBlocks_EmptyToolTreeSkipped verifies that when toolTree
// is empty, only the narration message is produced (no empty toolsummary).
func TestFinalizeTurnBlocks_EmptyToolTreeSkipped(t *testing.T) {
	m := &Model{
		narration: "理解了渲染逻辑",
		toolTree:  nil,
		messages:  []DisplayMessage{},
	}

	m.finalizeTurnBlocks()

	if len(m.messages) != 1 {
		t.Fatalf("expected 1 message (narration only), got %d", len(m.messages))
	}
	if m.messages[0].Role != "narration" {
		t.Errorf("message role = %q, want narration", m.messages[0].Role)
	}
}

// TestFinalizeTurnBlocks_BothEmpty verifies that calling finalizeTurnBlocks
// with both narration and toolTree empty produces no messages.
func TestFinalizeTurnBlocks_BothEmpty(t *testing.T) {
	m := &Model{
		narration: "",
		toolTree:  nil,
		messages:  []DisplayMessage{},
	}

	m.finalizeTurnBlocks()

	if len(m.messages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(m.messages))
	}
}

// TestFinalizeTurnBlocks_PreservesExistingMessages verifies that
// finalizeTurnBlocks appends to existing messages without clearing them.
func TestFinalizeTurnBlocks_PreservesExistingMessages(t *testing.T) {
	m := &Model{
		narration: "下一步搜索",
		toolTree:  []ToolNode{{Name: "read", Detail: "file.go", Done: true}},
		messages: []DisplayMessage{
			{Role: "user", Content: "查找"},
			{Role: "narration", Content: "之前的叙述"},
		},
	}

	m.finalizeTurnBlocks()

	if len(m.messages) != 4 {
		t.Fatalf("expected 4 messages (2 existing + narration + toolsummary), got %d", len(m.messages))
	}
	if m.messages[0].Content != "查找" {
		t.Errorf("first message should be preserved, got %q", m.messages[0].Content)
	}
	if m.messages[1].Content != "之前的叙述" {
		t.Errorf("second message should be preserved, got %q", m.messages[1].Content)
	}
	if m.messages[2].Role != "narration" {
		t.Errorf("third message role = %q, want narration", m.messages[2].Role)
	}
	if m.messages[3].Role != "toolsummary" {
		t.Errorf("fourth message role = %q, want toolsummary", m.messages[3].Role)
	}
}
