package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// researchUntilNudgedModel returns read tool calls (varying paths, so per-file
// loop detection never fires) until the request history carries the budget-tail
// wrap-up nudge, then produces a conclusion. It models the "/collab stage
// times out" failure: a research sub-agent that keeps exploring with tools and,
// without a budget-aware nudge, exhausts its iteration cap with no conclusion.
type researchUntilNudgedModel struct {
	calls   int
	nudged  bool
	lastReq ModelRequest
}

func (m *researchUntilNudgedModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *researchUntilNudgedModel) Complete(_ context.Context, req ModelRequest) (*ModelResponse, error) {
	m.calls++
	m.lastReq = req
	if !m.nudged {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "只剩") || strings.Contains(msg.Content, "iterations left") {
				m.nudged = true
				break
			}
		}
	}
	if m.nudged {
		if toolsContain(req.Tools, SubmitResultToolName) {
			return &ModelResponse{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID: "s1", Type: "function",
				Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"根因已定位：预算耗尽前收到收尾提示后收敛。"}`},
			}}}, FinishReason: "tool_calls"}, nil
		}
		return &ModelResponse{Message: ModelMessage{Role: "assistant", Content: "### 结论\n根因已定位：预算耗尽前收到收尾提示后收敛。"}, FinishReason: "stop"}, nil
	}
	return &ModelResponse{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
		ID: "c1", Type: "function",
		Function: ModelFunctionCall{Name: "read", Arguments: fmt.Sprintf(`{"path":"x%d.go"}`, m.calls)},
	}}}, FinishReason: "tool_calls"}, nil
}

// deadlineProbeModel records whether the ctx passed to Complete carried a
// deadline. Locks the per-call 120s total-duration timeout removal: the
// sub-agent loop must pass through the parent ctx with NO synthetic deadline —
// the underlying client's idle timeout (DefaultIdleTimeout=60s, aborts only
// when no SSE data line arrives) is the real hang guard. A total-duration
// deadline mis-kills normal slow streaming (large context, long generation),
// surfacing as "(sub-agent error: context deadline exceeded)".
type deadlineProbeModel struct {
	gotDeadline bool
}

func (m *deadlineProbeModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *deadlineProbeModel) Complete(ctx context.Context, _ ModelRequest) (*ModelResponse, error) {
	_, m.gotDeadline = ctx.Deadline()
	return &ModelResponse{Message: ModelMessage{Role: "assistant", Content: "结论"}, FinishReason: "stop"}, nil
}

// TestSubAgent_NoSyntheticPerCallDeadline locks the "(sub-agent error: context
// deadline exceeded)" failure: sub_agent.go wrapped each Complete call in
// context.WithTimeout(ctx, 120s), a TOTAL-duration limit. A streaming model
// keeps returning content, but a large-context / long-generation call can still
// exceed 120s total and get killed — both /collab design and dev stages hit it.
// The loop must pass the parent ctx through untouched; the client's own idle
// timeout (no SSE data line for 60s) is the true hang guard.
func TestSubAgent_NoSyntheticPerCallDeadline(t *testing.T) {
	model := &deadlineProbeModel{}
	runner := &SubAgentRunner{model: model, tools: &recordingToolExecutor{}, modelName: "test"}
	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "审查", NoNudge: true, UserLanguage: "中文",
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Errorf("expected FinishReason=%q, got %q", HandoffReasonCompleted, result.FinishReason)
	}
	if model.gotDeadline {
		t.Error("Complete must receive the parent ctx WITHOUT a synthetic per-call deadline (120s total-duration timeout removed)")
	}
}

// TestSubAgent_BudgetTailNudge_ConvergesToConclusion locks the "/collab stage
// produces '(analysis timed out, partial result)' with no conclusion" bug: a
// research sub-agent with a finite iteration budget keeps calling read tools
// and, without a wrap-up nudge near the end of the budget, exhausts the cap
// into a fallback summary (FinishReason=max_iterations, TimedOut=true) instead
// of a real conclusion.
func TestSubAgent_BudgetTailNudge_ConvergesToConclusion(t *testing.T) {
	model := &researchUntilNudgedModel{}
	runner := &SubAgentRunner{model: model, tools: &recordingToolExecutor{}, modelName: "test"}
	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研 codex 与 deepact 的区别", MaxIterations: 6, NoNudge: true, UserLanguage: "中文",
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Errorf("expected FinishReason=%q (converged), got %q", HandoffReasonCompleted, result.FinishReason)
	}
	if result.TimedOut {
		t.Errorf("expected TimedOut=false, got true")
	}
	if !strings.Contains(result.Summary, "根因已定位") {
		t.Errorf("expected the model's conclusion as Summary, got %q", result.Summary)
	}
	// The run must conclude within the budget, not at the hard cap.
	if model.calls >= 6 {
		t.Errorf("expected conclusion before exhausting the 6-turn budget, used %d calls", model.calls)
	}
}

// TestSubAgent_BudgetTailNudge_StructuredNamesSubmitResult: in a structured run
// the budget-tail nudge must direct the model to submit_result, so the injected
// wrap-up instruction is unambiguous (a plain text reply never completes a
// structured run).
func TestSubAgent_BudgetTailNudge_StructuredNamesSubmitResult(t *testing.T) {
	model := &researchUntilNudgedModel{}
	runner := &SubAgentRunner{model: model, tools: &recordingToolExecutor{}, modelName: "test"}
	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "审查", MaxIterations: 5, StructuredResult: true, UserLanguage: "中文",
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Errorf("expected FinishReason=%q, got %q", HandoffReasonCompleted, result.FinishReason)
	}
	if model.calls >= 5 {
		t.Errorf("expected conclusion before exhausting the 5-turn budget, used %d calls", model.calls)
	}
	found := false
	for _, msg := range model.lastReq.Messages {
		if strings.Contains(msg.Content, "submit_result") &&
			(strings.Contains(msg.Content, "只剩") || strings.Contains(msg.Content, "iterations left")) {
			found = true
		}
	}
	if !found {
		t.Errorf("structured budget-tail nudge must name submit_result, last request messages: %q", model.lastReq.Messages)
	}
}
