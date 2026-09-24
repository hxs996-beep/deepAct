package engine

import (
	"context"
	"strings"
	"testing"
)

// TestExecuteTurn_RepeatFactAnnotatedNotBlocked locks the per-target repeat
// policy: a repeated tool call is reported to the model as an objective fact
// appended to the tool result. It must never block the call or end the turn —
// the model decides whether to change approach or conclude from what it
// already has.
func TestExecuteTurn_RepeatFactAnnotatedNotBlocked(t *testing.T) {
	model := &stubStreamModel{chunks: []ModelChunk{{
		Delta: "读两次",
		ToolCalls: []ModelToolCall{
			{ID: "c1", Type: "function", Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"a.go"}`}},
			{ID: "c2", Type: "function", Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"a.go"}`}},
		},
		FinishReason: "tool_calls",
	}}}
	e := &Engine{
		model:   model,
		tools:   &recordingToolExecutor{},
		context: &stubContextBuilder{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "读"}},
		config:  EngineConfig{ModelName: "test-model", WorkDir: "/repo"},
		guards:  &GuardSystem{scope: NewScopeGuard()},
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Blocked {
		t.Fatalf("repeated read must not block the turn, got BlockedBy=%q", result.BlockedBy)
	}

	var annotated int
	for _, m := range e.history {
		if m.Role == "tool" && strings.Contains(m.Content, "[repeat 2× this run:") {
			annotated++
		}
	}
	if annotated != 1 {
		t.Errorf("expected exactly the second read to carry the repeat fact, got %d (history=%+v)", annotated, e.history)
	}
}

// TestExecuteTurn_NovelReadsNotAnnotated: distinct targets never carry the
// repeat fact — only a genuine repeat does.
func TestExecuteTurn_NovelReadsNotAnnotated(t *testing.T) {
	model := &stubStreamModel{chunks: []ModelChunk{{
		Delta: "读两个文件",
		ToolCalls: []ModelToolCall{
			{ID: "c1", Type: "function", Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"a.go"}`}},
			{ID: "c2", Type: "function", Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"b.go"}`}},
		},
		FinishReason: "tool_calls",
	}}}
	e := &Engine{
		model:   model,
		tools:   &recordingToolExecutor{},
		context: &stubContextBuilder{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "读"}},
		config:  EngineConfig{ModelName: "test-model", WorkDir: "/repo"},
		guards:  &GuardSystem{scope: NewScopeGuard()},
	}

	if _, err := e.executeTurn(context.Background()); err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	for _, m := range e.history {
		if m.Role == "tool" && strings.Contains(m.Content, "[repeat") {
			t.Errorf("novel reads must not carry a repeat fact, got %q", m.Content)
		}
	}
}
