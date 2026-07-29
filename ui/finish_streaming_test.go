package ui

import (
	"testing"

	"github.com/deepact/deepact/engine"
)

// TestFinishStreaming_SummarySuppressesNarrationDuplication verifies that when
// both narration (streaming content_delta) and Summary (from task_complete)
// are present, the text appears exactly once. Previously finalizeTurnBlocks
// unconditionally snapshotted narration before the Summary check, causing the
// same text to be displayed twice.
func TestFinishStreaming_SummarySuppressesNarrationDuplication(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateReady,
		msgCache: &messageRenderCache{},
	}
	dupText := "分析完成，以下是结果。"
	m.narration = dupText
	m.finishStreaming(EngineResponseMsg{
		Response: &engine.EngineResponse{Summary: dupText},
	})

	count := 0
	for _, msg := range m.messages {
		if (msg.Role == "assistant" || msg.Role == "narration") && msg.Content == dupText {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected duplicated text to appear exactly once, got %d. messages: %+v", count, m.messages)
	}
}

// TestFinishStreaming_SummaryStripsPreSnapshottedNarration verifies that
// narration snapshotted by finalizeTurnBlocks at tool_start (before
// finishStreaming runs) is stripped when Summary is present. This is the
// real-world bug: LLM generates narration text, calls a regular tool
// (triggering tool_start -> finalizeTurnBlocks snapshots narration into
// m.messages), then calls task_complete with the same text as Summary.
// Without stripRunNarration, both the narration snapshot and the Summary
// appear, duplicating the text.
func TestFinishStreaming_SummaryStripsPreSnapshottedNarration(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateReady,
		msgCache: &messageRenderCache{},
	}
	// Simulate: user sent a message, runStartMsgIdx is set
	m.runStartMsgIdx = 0
	m.messages = []DisplayMessage{{Role: "user", Content: "OK"}}

	// Simulate: LLM streamed narration text via content_delta
	dupText := "用户已确认设计方案。按 brainstorming 流程，先写设计文档，然后转入实现。"
	m.narration = dupText

	// Simulate: LLM called a regular tool -> tool_start -> finalizeTurnBlocks
	// snapshots narration into m.messages as Role:"narration"
	m.finalizeTurnBlocks()

	// Verify narration was snapshotted
	narrationCount := 0
	for _, msg := range m.messages {
		if msg.Role == "narration" && msg.Content == dupText {
			narrationCount++
		}
	}
	if narrationCount != 1 {
		t.Fatalf("precondition: expected 1 narration snapshot, got %d", narrationCount)
	}

	// Simulate: LLM called task_complete with same text as Summary
	m.finishStreaming(EngineResponseMsg{
		Response: &engine.EngineResponse{Summary: dupText},
	})

	// Verify: dupText should appear exactly once (as assistant, not narration)
	count := 0
	for _, msg := range m.messages {
		if (msg.Role == "assistant" || msg.Role == "narration") && msg.Content == dupText {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected pre-snapshotted narration to be stripped, text should appear exactly once, got %d. messages: %+v", count, m.messages)
	}

	// Verify: no narration role messages remain from this Run
	for _, msg := range m.messages {
		if msg.Role == "narration" {
			t.Errorf("narration message should have been stripped, got: %+v", msg)
		}
	}
}

// TestFinishStreaming_NarrationUsedWhenNoSummary verifies that narration is
// still displayed when Summary is absent (normal multi-turn narration path).
func TestFinishStreaming_NarrationUsedWhenNoSummary(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateReady,
		msgCache: &messageRenderCache{},
	}
	narrationText := "正在分析中..."
	m.narration = narrationText
	m.finishStreaming(EngineResponseMsg{
		Response: &engine.EngineResponse{},
	})

	found := false
	for _, msg := range m.messages {
		if msg.Role == "narration" && msg.Content == narrationText {
			found = true
		}
	}
	if !found {
		t.Errorf("narration should be snapshot when no Summary, messages: %+v", m.messages)
	}
}
