package engine

import (
	"context"
	"testing"
)

// assertNoContentlessAssistant fails when history holds an assistant message
// with neither visible text nor tool calls. Such a message serializes without a
// `content` field (Content is omitempty) and the API rejects the whole request
// with 400 "Invalid assistant message: content or tool_calls must be set" —
// permanently poisoning every later request in the session.
func assertNoContentlessAssistant(t *testing.T, history []Message) {
	t.Helper()
	for i, m := range history {
		if m.Role == "assistant" && m.Content == "" && len(m.ToolCalls) == 0 {
			t.Fatalf("history[%d] is a content-less assistant message (no content, no tool_calls): %+v", i, m)
		}
	}
}

// TestExecuteTurn_ReasoningOnlyTurnNotPersisted: 模型只输出思考（reasoning_content）、
// 没有正文也没有工具调用时，不得把该 assistant 消息写进历史。
func TestExecuteTurn_ReasoningOnlyTurnNotPersisted(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{ReasoningDelta: "让我想想……", FinishReason: "stop"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "hi"}},
		config:  EngineConfig{ModelName: "test-model"},
	}

	if _, err := e.executeTurn(context.Background()); err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	assertNoContentlessAssistant(t, e.history)
}

// TestExecuteTurn_ReasoningOnlyLengthTurnNotPersisted: 输出被 max_tokens 截断
// （finish=="length"）的续写路径同样不得写入，但仍然要 pin 下"继续"。
func TestExecuteTurn_ReasoningOnlyLengthTurnNotPersisted(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{ReasoningDelta: "想到一半被截断", FinishReason: "length"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "hi"}},
		config:  EngineConfig{ModelName: "test-model"},
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	assertNoContentlessAssistant(t, e.history)
	if result.Done {
		t.Errorf("a length-truncated turn must not end the run, got %+v", result)
	}
	if len(e.pendingPinnedMessages) == 0 {
		t.Errorf("length truncation must still pin the 继续 nudge")
	}
}

// TestExecuteTurn_TextTurnStillPersisted: 有正文的回复必须照常写入（防止修过头）。
func TestExecuteTurn_TextTurnStillPersisted(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "答案正文", ReasoningDelta: "思考过程", FinishReason: "stop"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "hi"}},
		config:  EngineConfig{ModelName: "test-model"},
	}

	if _, err := e.executeTurn(context.Background()); err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if len(e.history) != 2 {
		t.Fatalf("len(history) = %d, want 2 (user + assistant)", len(e.history))
	}
	last := e.history[1]
	if last.Role != "assistant" || last.Content != "答案正文" {
		t.Errorf("history[1] = %+v, want assistant content=答案正文", last)
	}
	if last.ReasoningContent != "思考过程" {
		t.Errorf("reasoning must be kept alongside content (tools require the echo), got %q", last.ReasoningContent)
	}
}

// TestExecuteTurn_SteerInterruptReasoningOnlyNotPersisted: steer 中断发生在模型
// 只产出思考、工具调用尚未成型（name 为空）时，同样不得写入 content-less assistant。
func TestExecuteTurn_SteerInterruptReasoningOnlyNotPersisted(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{ReasoningDelta: "只收到思考"},
			// A second chunk keeps the stream open long enough for the queued
			// steer to be observed at the top of the next iteration.
			{ReasoningDelta: "更多思考"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "hi"}},
		config:  EngineConfig{ModelName: "test-model"},
	}
	// Queue the steer while the first reasoning chunk is being processed, so it
	// is observed on the next chunk — exactly the mid-stream interruption case.
	queued := false
	e.config.OnProgress = func(ev ProgressEvent) {
		if ev.Type == "reasoning_delta" && !queued {
			queued = true
			e.Steer("换个方向")
		}
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !queued {
		t.Fatal("test setup: the steer was never queued")
	}
	if result.Done {
		t.Errorf("steer interrupt must keep the run going, got %+v", result)
	}
	assertNoContentlessAssistant(t, e.history)
}
