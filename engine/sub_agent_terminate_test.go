package engine

import (
	"context"
	"strings"
	"testing"
)

// stubSeqModel returns scripted ModelResponses in sequence on each Complete
// call, cycling back to the first once exhausted. Sub-agents use Complete (not
// Stream), so Stream is a no-op. The cycling simulates a model that alternates
// between tool calls and text - the exact loop the critic falls into when its
// verdict gets nudged.
type stubSeqModel struct {
	responses      []ModelResponse
	classifierResp string // returned on JsonMode=true calls (ConclusionClassifier probes)
	calls          int    // counts non-classifier (scripted) calls only
}

func (m *stubSeqModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *stubSeqModel) Complete(_ context.Context, req ModelRequest) (*ModelResponse, error) {
	// ConclusionClassifier probes use JsonMode; return the scripted verdict
	// without advancing the scripted call counter.
	if req.JsonMode {
		if m.classifierResp == "" {
			return &ModelResponse{}, nil
		}
		return &ModelResponse{Message: ModelMessage{Content: m.classifierResp}}, nil
	}
	if len(m.responses) == 0 {
		m.calls++
		return &ModelResponse{}, nil
	}
	resp := m.responses[m.calls%len(m.responses)]
	m.calls++
	return &resp, nil
}

// TestSubAgentRunLoop_TerminatesOnConclusionNotLoop reproduces the critic
// "can't stop" bug. The sub-agent calls a verification tool, then emits its
// verdict ("结论：失败 / VERDICT: FAIL") as a text-only response. That verdict
// is a genuine conclusion, so the loop must terminate immediately.
//
// Before the fix, the text-only branch nudged unconditionally; the nudge
// ("use tools to take the next action") goaded the critic into another tool
// call, resetting consecutiveIntermediate, so the loop ran to maxIterations
// (observed in production as "turn 90" of a 99-iteration cap).
func TestSubAgentRunLoop_TerminatesOnConclusionNotLoop(t *testing.T) {
	toolCall := ModelResponse{
		Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID:       "c1",
			Type:     "function",
			Function: ModelFunctionCall{Name: "bash", Arguments: `{"command":"go build ./..."}`},
		}}},
		FinishReason: "tool_calls",
	}
	verdict := ModelResponse{
		Message: ModelMessage{
			Role:    "assistant",
			Content: "构建失败。结论：失败\n\nVERDICT: FAIL",
		},
		FinishReason: "stop",
	}
	// Cycle [tool, verdict, tool, verdict, ...]: with the bug this loops to
	// maxIterations; with the fix it stops at the first verdict.
	model := &stubSeqModel{responses: []ModelResponse{toolCall, verdict}, classifierResp: `{"conclusion": true}`}

	runner := &SubAgentRunner{
		model:     model,
		tools:     stubToolExecutor{},
		modelName: "test",
	}

	result, err := runner.Run(context.Background(), Handoff{
		Agent:         AgentCritic,
		Goal:          "验证实现",
		MaxIterations: 8, // bounded well below 99 so the bug shows as a failed assertion, not a hang
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}

	// Fix: tool (call 1) -> verdict terminates (call 2). Exactly 2 calls.
	if model.calls != 2 {
		t.Errorf("expected loop to terminate at the verdict (2 model calls), got %d - the critic looped to maxIterations", model.calls)
	}
	if result.TimedOut {
		t.Errorf("expected TimedOut=false (terminated on conclusion), got TimedOut=true")
	}
	if !strings.Contains(result.Summary, "VERDICT: FAIL") {
		t.Errorf("expected Summary to contain the verdict, got: %q", result.Summary)
	}
}

// TestSubAgentRunLoop_NudgesOnNextStepNarration ensures the fix does not
// over-terminate: a forward-looking narration (no tool calls) still gets
// nudged rather than treated as a conclusion.
func TestSubAgentRunLoop_NudgesOnNextStepNarration(t *testing.T) {
	narration := ModelResponse{
		Message: ModelMessage{
			Role:    "assistant",
			Content: "查看 finishStreaming 逻辑，确认截断点。",
		},
		FinishReason: "stop",
	}
	model := &stubSeqModel{responses: []ModelResponse{narration}, classifierResp: `{"conclusion": false}`}

	runner := &SubAgentRunner{
		model:     model,
		tools:     stubToolExecutor{},
		modelName: "test",
	}

	result, err := runner.Run(context.Background(), Handoff{
		Agent:         AgentSub,
		Goal:          "分析截断问题",
		MaxIterations: 8,
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	// Narration cycles: 3 consecutive text-only turns -> break (3-strike).
	// It must NOT terminate on the first turn.
	if model.calls < 3 {
		t.Errorf("expected narration to be nudged (>=3 calls via 3-strike), got %d - over-terminated", model.calls)
	}
	if !strings.Contains(result.Summary, "截断") {
		t.Errorf("expected Summary to retain the narration content, got: %q", result.Summary)
	}
}

// TestSubAgentRunLoop_NudgesOnNextStepNarrationDespiteClassifierFalsePositive
// reproduces the "intermediate result, then stops" bug on the sub-agent path:
// the flash classifier WRONGLY judges a forward-looking narration as a
// conclusion (classifierResp=true). The sub-agent must still nudge instead of
// terminating on the first turn, because the text carries trailing next-step
// intent. Without the heuristic guard the sub-agent returns immediately
// (calls=1) on a partial answer.
func TestSubAgentRunLoop_NudgesOnNextStepNarrationDespiteClassifierFalsePositive(t *testing.T) {
	narration := ModelResponse{
		Message: ModelMessage{
			Role:    "assistant",
			Content: "查看 finishStreaming 逻辑，确认截断点。",
		},
		FinishReason: "stop",
	}
	model := &stubSeqModel{responses: []ModelResponse{narration}, classifierResp: `{"conclusion": true}`}

	runner := &SubAgentRunner{
		model:     model,
		tools:     stubToolExecutor{},
		modelName: "test",
	}

	result, err := runner.Run(context.Background(), Handoff{
		Agent:         AgentSub,
		Goal:          "分析截断问题",
		MaxIterations: 8,
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	// Must NOT terminate on the first turn despite the classifier false positive.
	if model.calls < 2 {
		t.Errorf("expected narration to be nudged despite classifier false positive (>=2 calls), got %d - over-terminated", model.calls)
	}
	if !strings.Contains(result.Summary, "截断") {
		t.Errorf("expected Summary to retain the narration content, got: %q", result.Summary)
	}
}
