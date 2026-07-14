package engine

import (
	"context"
	"testing"
)

// TestExecuteTurn_ReadMultiNotBlockedWhenNewFiles reproduces the bug where
// read_multi was ALWAYS blocked (with an empty LoopGuard message) even when
// every sub-target was a new file. Root cause: the read_multi branch declared
// `var loopAction GuardAction` (zero value, Type="") and only assigned it when
// a sub-target was blocked. When all sub-targets were Allow, loopAction stayed
// zero-valued, and `loopAction.Type != GuardAllow` ("" != "allow") was true,
// falsely blocking the call.
func TestExecuteTurn_ReadMultiNotBlockedWhenNewFiles(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta:        "读多个文件分析",
			FinishReason: "tool_calls",
			ToolCalls: []ModelToolCall{{
				ID:   "c1",
				Type: "function",
				Function: ModelFunctionCall{
					Name:      "read_multi",
					Arguments: "{\"targets\":[{\"path\":\"a.go\"},{\"path\":\"b.go\"}]}",
				},
			}},
		}}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0, Goal: "分析"},
		history: []Message{{Role: "user", Content: "分析"}},
		config:  EngineConfig{ModelName: "test"},
		guards:  &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Blocked {
		t.Errorf("read_multi with new files should NOT be blocked, got Blocked reason=%v msg=%q", result.BlockedBy, result.Questions)
	}
}
