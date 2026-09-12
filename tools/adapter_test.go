package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/deepact/deepact/engine"
)

type passthroughTool struct{}

func (passthroughTool) Spec() ToolSpec {
	return ToolSpec{Name: "echo", Description: "echo", Parameters: json.RawMessage(`{}`)}
}

func (passthroughTool) Run(ctx ToolContext, input json.RawMessage) (ToolResultEnvelope, error) {
	return ToolResultEnvelope{
		Status:       StatusOK,
		Digest:       "ok",
		FinishReason: "completed",
		Questions:    []string{"问题？"},
	}, nil
}

// TestAdapter_PassthroughCtxAndQuestions verifies the EngineExecutor adapter
// forwards Ctx/Depth/UserLang into ToolContext and FinishReason/Questions
// back into ToolResult.
func TestAdapter_PassthroughCtxAndQuestions(t *testing.T) {
	reg := NewRegistry()
	reg.Register(passthroughTool{})
	exec := NewEngineExecutor(reg)

	ctx := context.WithValue(context.Background(), "k", "v")
	results := exec.Execute(engine.ToolExecContext{
		WorkDir: "/tmp", SessionID: "s1", TurnNumber: 3,
		Ctx: ctx, Depth: 1, UserLang: "中文",
	}, []engine.ToolCallRequest{{ID: "c1", Name: "echo", Input: json.RawMessage(`{}`)}})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].FinishReason != "completed" {
		t.Errorf("FinishReason = %q, want completed", results[0].FinishReason)
	}
	if len(results[0].Questions) != 1 || results[0].Questions[0] != "问题？" {
		t.Errorf("Questions = %v, want [问题？]", results[0].Questions)
	}
}
