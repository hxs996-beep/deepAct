package ui

import (
	"strings"
	"testing"

	"github.com/deepact/deepact/engine"
)

// TestFinishStreaming_SummaryDuplicate_KeepsFormattedSummary verifies that when
// both narration (streaming content_delta) and Summary (from task_complete) are
// present and match, the FORMATTED assistant Summary is kept and the plain-text
// narration copy is removed — the text appears exactly once. The streamed
// narration is rendered as plain text (renderStreaming) and leaks raw markdown
// (tables, ---, **); keeping it as the final report leaves the user with the
// unformatted copy.
func TestFinishStreaming_SummaryDuplicate_KeepsFormattedSummary(t *testing.T) {
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
	foundAssistant := false
	for _, msg := range m.messages {
		if (msg.Role == "assistant" || msg.Role == "narration") && msg.Content == dupText {
			count++
		}
		if msg.Role == "assistant" && msg.Content == dupText {
			foundAssistant = true
		}
		if msg.Role == "narration" && msg.Content == dupText {
			t.Errorf("plain-text narration duplicating the Summary should be removed. messages: %+v", m.messages)
		}
	}
	if count != 1 {
		t.Errorf("expected duplicated text to appear exactly once, got %d. messages: %+v", count, m.messages)
	}
	if !foundAssistant {
		t.Errorf("formatted assistant Summary should be kept. messages: %+v", m.messages)
	}
}

// TestFinishStreaming_SummaryDuplicate_PreSnapshottedNarrationRemoved verifies
// that when narration was already snapshotted at tool_start (via
// finalizeTurnBlocks) and matches the Summary, the plain-text narration is
// removed and the formatted assistant Summary is kept instead. Only the
// current run's narration (from runStartMsgIdx onward) is removed — earlier
// messages are untouched.
func TestFinishStreaming_SummaryDuplicate_PreSnapshottedNarrationRemoved(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateReady,
		msgCache: &messageRenderCache{},
	}
	m.runStartMsgIdx = 0
	m.messages = []DisplayMessage{{Role: "user", Content: "OK"}}

	dupText := "用户已确认设计方案。按 brainstorming 流程，先写设计文档，然后转入实现。"
	m.narration = dupText
	m.finalizeTurnBlocks(false)

	narrationCount := 0
	for _, msg := range m.messages {
		if msg.Role == "narration" && msg.Content == dupText {
			narrationCount++
		}
	}
	if narrationCount != 1 {
		t.Fatalf("precondition: expected 1 narration snapshot, got %d", narrationCount)
	}

	m.finishStreaming(EngineResponseMsg{
		Response: &engine.EngineResponse{Summary: dupText},
	})

	count := 0
	foundAssistant := false
	for _, msg := range m.messages {
		if (msg.Role == "assistant" || msg.Role == "narration") && msg.Content == dupText {
			count++
		}
		if msg.Role == "assistant" && msg.Content == dupText {
			foundAssistant = true
		}
		if msg.Role == "narration" && msg.Content == dupText {
			t.Errorf("plain-text narration duplicating the Summary should be removed. messages: %+v", m.messages)
		}
	}
	if count != 1 {
		t.Errorf("expected text to appear exactly once, got %d. messages: %+v", count, m.messages)
	}
	if !foundAssistant {
		t.Errorf("formatted assistant Summary should be kept. messages: %+v", m.messages)
	}

	// The user message before the run must be preserved.
	if len(m.messages) == 0 || m.messages[0].Role != "user" || m.messages[0].Content != "OK" {
		t.Errorf("user message before the run should be preserved. messages: %+v", m.messages)
	}
}

// TestFinishStreaming_DifferentNarrationAndSummary_KeepsBoth verifies that
// when narration differs from Summary, both are kept so the user can see
// intermediate narration AND the final formatted Summary.
func TestFinishStreaming_DifferentNarrationAndSummary_KeepsBoth(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateReady,
		msgCache: &messageRenderCache{},
	}
	m.narration = "正在分析代码结构..."
	m.finishStreaming(EngineResponseMsg{
		Response: &engine.EngineResponse{Summary: "分析完成，问题已定位。"},
	})

	foundNarration := false
	foundSummary := false
	for _, msg := range m.messages {
		if msg.Role == "narration" && msg.Content == "正在分析代码结构..." {
			foundNarration = true
		}
		if msg.Role == "assistant" && msg.Content == "分析完成，问题已定位。" {
			foundSummary = true
		}
	}
	if !foundNarration {
		t.Error("narration should be preserved when different from Summary")
	}
	if !foundSummary {
		t.Error("Summary should be added when different from narration")
	}
}

// TestFinishStreaming_AwaitingUser_KeepsNarrationSkipsDuplicate verifies that
// when the engine blocks with BlockedBy="awaiting_user", the already-streamed
// narration (intermediate analysis + the question itself) is snapshotted and
// no duplicate assistant message is appended — the user sees the thinking
// process AND the question, exactly once.
func TestFinishStreaming_AwaitingUser_KeepsNarrationSkipsDuplicate(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateReady,
		msgCache: &messageRenderCache{},
	}
	questionText := "方案1、2、3 你选哪个？"
	m.narration = "分析发现两个问题需要决策。\n" + questionText
	m.finishStreaming(EngineResponseMsg{
		Response: &engine.EngineResponse{
			Blocked:   true,
			BlockedBy: "awaiting_user",
			Questions: []string{questionText},
		},
	})

	// Narration (thinking process + question) must be preserved as a message.
	foundNarration := false
	assistantCount := 0
	for _, msg := range m.messages {
		if msg.Role == "narration" && strings.Contains(msg.Content, questionText) {
			foundNarration = true
		}
		if msg.Role == "assistant" {
			assistantCount++
		}
	}
	if !foundNarration {
		t.Errorf("expected narration (with question) to be preserved, messages: %+v", m.messages)
	}
	if assistantCount != 0 {
		t.Errorf("expected NO duplicate assistant message for awaiting_user, got %d assistant messages: %+v", assistantCount, m.messages)
	}
}

// TestFinishStreaming_AwaitingUser_NoNarration_AppendsQuestion verifies that
// when nothing was streamed (empty narration), the awaiting_user question is
// still shown as an assistant message — the user must see the question.
func TestFinishStreaming_AwaitingUser_NoNarration_AppendsQuestion(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateReady,
		msgCache: &messageRenderCache{},
	}
	questionText := "是否继续深入排查？"
	m.finishStreaming(EngineResponseMsg{
		Response: &engine.EngineResponse{
			Blocked:   true,
			BlockedBy: "awaiting_user",
			Questions: []string{questionText},
		},
	})

	found := false
	for _, msg := range m.messages {
		if msg.Role == "assistant" && strings.Contains(msg.Content, questionText) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected question in assistant message when narration empty, messages: %+v", m.messages)
	}
}

func TestFinishStreaming_MarkdownNarrationMatchesPlainSummary(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateReady,
		msgCache: &messageRenderCache{},
	}
	m.narration = "## 分析结果\n\n**问题已修复**"
	m.finishStreaming(EngineResponseMsg{
		Response: &engine.EngineResponse{Summary: "分析结果\n\n问题已修复"},
	})

	count := 0
	for _, msg := range m.messages {
		if msg.Role == "narration" || msg.Role == "assistant" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 message (narration or assistant) for matching content, got %d. messages: %+v", count, m.messages)
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

// TestFinishStreaming_NonBlockedOptionsShowPopup verifies that the analysis-gate
// confirmation popup appears even when the engine returns Options on a
// non-Blocked response. The engine attaches Options at Run end (loop.go:932)
// without setting Blocked, so finishStreaming must consume them outside the
// Blocked branch — otherwise the popup never renders and the user sees the
// agent's "wait for confirmation" text with no way to confirm.
func TestFinishStreaming_NonBlockedOptionsShowPopup(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateReady,
		msgCache: &messageRenderCache{},
	}
	m.finishStreaming(EngineResponseMsg{
		Response: &engine.EngineResponse{
			Summary: "报告完毕，停止。请通过确认 UI 批准后，我立即执行全部删除。",
			Options: []string{
				"方案A: 按报告执行修改",
				"其他（输入你的意见）",
			},
		},
	})

	if len(m.activeOptions) != 2 {
		t.Errorf("expected activeOptions to be set from non-Blocked response, got %d: %v", len(m.activeOptions), m.activeOptions)
	}
	if m.selectedOption != 0 {
		t.Errorf("expected selectedOption reset to 0, got %d", m.selectedOption)
	}
}
