package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/deepact/deepact/engine"
)

// ctxKey is a typed key for context values in this test; using a bare string
// key would trigger staticcheck SA1029.
type ctxKey string

const passthroughCtxKey ctxKey = "k"

// passthroughTool captures the ToolContext it receives so tests can assert the
// adapter forwards Ctx/Depth/UserLang into tools.
type passthroughTool struct {
	receivedCtx      context.Context
	receivedDepth    int
	receivedUserLang string
}

func (t *passthroughTool) Spec() ToolSpec {
	return ToolSpec{Name: "echo", Description: "echo", Parameters: json.RawMessage(`{}`)}
}

func (t *passthroughTool) Run(ctx ToolContext, input json.RawMessage) (ToolResultEnvelope, error) {
	t.receivedCtx = ctx.Ctx
	t.receivedDepth = ctx.Depth
	t.receivedUserLang = ctx.UserLang
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
	tool := &passthroughTool{}
	reg.Register(tool)
	exec := NewEngineExecutor(reg)

	ctx := context.WithValue(context.Background(), passthroughCtxKey, "v")
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

	// Forward passthrough: the tool must receive the exact Ctx, Depth and
	// UserLang the caller passed to the EngineExecutor adapter.
	if tool.receivedCtx == nil {
		t.Error("tool received nil Ctx")
	} else if got := tool.receivedCtx.Value(passthroughCtxKey); got != "v" {
		t.Errorf("tool received Ctx value = %v, want %q at key %q", got, "v", passthroughCtxKey)
	}
	if tool.receivedDepth != 1 {
		t.Errorf("tool received Depth = %d, want 1", tool.receivedDepth)
	}
	if tool.receivedUserLang != "中文" {
		t.Errorf("tool received UserLang = %q, want 中文", tool.receivedUserLang)
	}
}
