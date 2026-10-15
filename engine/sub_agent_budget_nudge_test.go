package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

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

// textAfterToolsModel answers once with a read call and then ALWAYS replies in
// plain prose — a researcher's natural deliverable — without ever calling
// submit_result.
type textAfterToolsModel struct {
	calls   int
	lastReq ModelRequest
}

func (m *textAfterToolsModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *textAfterToolsModel) Complete(_ context.Context, req ModelRequest) (*ModelResponse, error) {
	m.calls++
	m.lastReq = req
	if m.calls == 1 {
		return &ModelResponse{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID: "c1", Type: "function",
			Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"x.go"}`},
		}}}, FinishReason: "tool_calls"}, nil
	}
	return &ModelResponse{Message: ModelMessage{
		Role: "assistant", Content: "调研结论：这是散文形式的最终报告。",
	}, FinishReason: "stop"}, nil
}

// TestResearcherRole_ProseAnswerIsRedirectedToSubmitResult locks fix #1 end to
// end through the REAL built-in role: when the researcher replies with its
// finished report as prose, the engine must nudge it toward submit_result (the
// deterministic terminal) instead of "use tools" — the nudge that made collab
// researchers keep exploring and end as "stalled narration (partial result)".
func TestResearcherRole_ProseAnswerIsRedirectedToSubmitResult(t *testing.T) {
	model := &textAfterToolsModel{}
	runner := NewSubAgentRunner(model, &recordingToolExecutor{}, NewAgentRegistry(), "test")
	a := &specSubAgent{runner: runner, spec: researcherSpec()}

	result, err := a.Run(context.Background(), Handoff{Agent: AgentResearcher, Goal: "调研某个方向"})
	if err != nil {
		t.Fatalf("researcher run: %v", err)
	}
	// Without submit_result the run ends as no_result — NOT as a narration
	// stall — and the prose it produced is still returned as the summary.
	if result.FinishReason != HandoffReasonNoResult {
		t.Errorf("FinishReason = %q, want %q (a dashed-off prose answer must be redirected, not treated as narration)", result.FinishReason, HandoffReasonNoResult)
	}
	if !strings.Contains(result.Summary, "调研结论") {
		t.Errorf("the researcher's prose must still come back as the summary, got %q", result.Summary)
	}
	nudgedSubmit, nudgedTools := false, false
	for _, msg := range model.lastReq.Messages {
		if strings.Contains(msg.Content, SubmitResultToolName) {
			nudgedSubmit = true
		}
		if strings.Contains(msg.Content, "Use tools to take the next action") {
			nudgedTools = true
		}
	}
	if !nudgedSubmit {
		t.Error("the follow-up request must carry the submit_result nudge")
	}
	if nudgedTools {
		t.Error("a structured researcher must NOT be nudged to keep using tools")
	}
	if !toolsContain(model.lastReq.Tools, "read") {
		t.Error("the researcher must still be offered its read-only tools")
	}
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
		Agent: AgentSub, Goal: "审查", TextResult: true})
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

// tokenBudgetNudgeMarker is the wrap-up nudge's stable wording (see
// tokenBudgetNudge). With no turn cap this nudge is a run's only budget signal,
// so the model below keys on it.
const tokenBudgetNudgeMarker = "tokens remain in this run's budget"

// tokenBudgetUntilNudgedModel keeps returning read tool calls (varying paths, so
// per-file loop detection never fires) until the request history carries the
// token-budget wrap-up nudge, then delivers its conclusion — through
// submit_result when the run is structured, as plain text when the role declared
// its text reply to be the result. It models the "/collab stage produces a
// partial result with no conclusion" failure: a research sub-agent that keeps
// exploring and needs to be told to wrap up when its budget runs low.
//
// Every turn reports usage, the terminal one included: the run must converge
// BEFORE the budget gate (spent >= budget returns budget_exceeded ahead of the
// submit branch), and the test must not assume a provider reports nothing for
// the final call.
type tokenBudgetUntilNudgedModel struct {
	calls   int
	nudged  bool
	lastReq ModelRequest
}

// nudgeTurnUsage is the billable usage charged per turn: with a 1000-token
// run budget the 80% mark is crossed at the end of turn 8, so the nudge lands
// at turn 9.
const nudgeTurnUsage = 100

func (m *tokenBudgetUntilNudgedModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *tokenBudgetUntilNudgedModel) Complete(_ context.Context, req ModelRequest) (*ModelResponse, error) {
	m.calls++
	m.lastReq = req
	if !m.nudged {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, tokenBudgetNudgeMarker) {
				m.nudged = true
				break
			}
		}
	}
	usage := ModelUsage{CacheMissTokens: nudgeTurnUsage, TotalTokens: nudgeTurnUsage}
	if m.nudged {
		if toolsContain(req.Tools, SubmitResultToolName) {
			return &ModelResponse{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID: "s1", Type: "function",
				Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"预算收尾提示后收敛。"}`},
			}}}, FinishReason: "tool_calls", Usage: usage}, nil
		}
		return &ModelResponse{Message: ModelMessage{
			Role: "assistant", Content: "结论：预算收尾提示后收敛。",
		}, FinishReason: "stop", Usage: usage}, nil
	}
	return &ModelResponse{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
		ID: "c1", Type: "function",
		Function: ModelFunctionCall{Name: "read", Arguments: fmt.Sprintf(`{"path":"x%d.go"}`, m.calls)},
	}}}, FinishReason: "tool_calls", Usage: usage}, nil
}

// TestSubAgent_TokenBudgetNudge_StructuredNamesSubmitResult locks the wrap-up
// nudge that replaced the turn-cap nudge: a structured run crossing 80% of its
// token budget must be told to stop exploring and call submit_result (plain text
// never completes a structured run), and it must converge BEFORE the budget gate
// ends the run as budget_exceeded.
func TestSubAgent_TokenBudgetNudge_StructuredNamesSubmitResult(t *testing.T) {
	model := &tokenBudgetUntilNudgedModel{}
	runner := budgetRunnerFor(model, readSpecExec(), 1000)
	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "审查", StructuredResult: true})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Errorf("expected FinishReason=%q (converged), got %q", HandoffReasonCompleted, result.FinishReason)
	}
	// 100 spent per turn: 80% of 1000 is crossed at the end of turn 8, so the
	// nudge is injected at the top of turn 9 and the run submits there (900 of
	// 1000 spent, still under the hard gate).
	if model.calls != 9 {
		t.Errorf("expected 9 calls (8 exploring + 1 after the nudge), got %d", model.calls)
	}
	found := false
	for _, msg := range model.lastReq.Messages {
		if strings.Contains(msg.Content, tokenBudgetNudgeMarker) &&
			strings.Contains(msg.Content, SubmitResultToolName) {
			found = true
		}
	}
	if !found {
		t.Errorf("the token-budget nudge must name submit_result, last request messages: %q", model.lastReq.Messages)
	}
}

// TestSubAgent_TokenBudgetNudge_TextResultConverges locks the other half of the
// same nudge: for a role that declared its text reply to be the result
// (TextResult), the nudge must NOT name submit_result (that tool is not even
// offered) and the run must still converge on a real result before the budget
// gate. This is the path that lost its coverage when the old turn-cap
// budget-tail test was removed.
func TestSubAgent_TokenBudgetNudge_TextResultConverges(t *testing.T) {
	model := &tokenBudgetUntilNudgedModel{}
	runner := budgetRunnerFor(model, readSpecExec(), 1000)
	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研", TextResult: true})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Errorf("expected FinishReason=%q (converged), got %q", HandoffReasonCompleted, result.FinishReason)
	}
	if model.calls != 9 {
		t.Errorf("expected 9 calls (8 exploring + 1 after the nudge), got %d", model.calls)
	}
	if !strings.Contains(result.Summary, "预算收尾提示后收敛") {
		t.Errorf("expected the conclusion produced after the nudge as Summary, got %q", result.Summary)
	}
	for _, msg := range model.lastReq.Messages {
		if strings.Contains(msg.Content, tokenBudgetNudgeMarker) &&
			strings.Contains(msg.Content, SubmitResultToolName) {
			t.Errorf("a non-structured run must not be pointed at submit_result, message: %q", msg.Content)
		}
	}
}
