package engine

import (
	"context"
	"strings"
	"testing"
)

// recordingToolExecutor records which tools actually executed, so a test can
// assert that a truncated response's tool calls were never dispatched.
type recordingToolExecutor struct {
	executed []string
}

func (r *recordingToolExecutor) Execute(_ ToolExecContext, calls []ToolCallRequest) []ToolResult {
	var out []ToolResult
	for _, c := range calls {
		r.executed = append(r.executed, c.Name)
		out = append(out, ToolResult{ToolCallID: c.ID, ToolName: c.Name, Status: "ok", Digest: "ok"})
	}
	return out
}

func (r *recordingToolExecutor) Specs() []ModelTool {
	return []ModelTool{{Type: "function", Function: ModelToolFunction{Name: "bash"}}}
}

// TestSubAgentTruncation_NeverEndsOnPartialText: a response cut off by the
// output cap (finish_reason == "length") must never be treated as a
// conclusion — neither by the ConclusionClassifier nor by the NoNudge path.
// After 3 consecutive truncations the loop gives up with reason="max_tokens"
// carrying the partial text.
func TestSubAgentTruncation_NeverEndsOnPartialText(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", Content: "半截结论"}, FinishReason: "length"},
	}}

	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}
	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if model.calls != 3 {
		t.Errorf("expected 3 calls (3 truncation strikes), got %d", model.calls)
	}
	if model.classifierCalls != 0 {
		t.Errorf("expected classifier never probed on truncated replies, got %d probe(s)", model.classifierCalls)
	}
	if result.FinishReason != HandoffReasonMaxTokens {
		t.Errorf("expected FinishReason=%q, got %q", HandoffReasonMaxTokens, result.FinishReason)
	}
	if !strings.Contains(result.Summary, "半截结论") {
		t.Errorf("expected partial text preserved in Summary, got %q", result.Summary)
	}
}

// TestSubAgentTruncation_DropsTruncatedToolCalls: a truncated response may
// carry a partially streamed tool call whose arguments never closed. The
// call must never be dispatched.
func TestSubAgentTruncation_DropsTruncatedToolCalls(t *testing.T) {
	exec := &recordingToolExecutor{}
	model := &stubSeqModel{responses: []ModelResponse{
		{
			Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID: "c1", Type: "function",
				Function: ModelFunctionCall{Name: "bash", Arguments: `{"command":"rm -rf"}`},
			}}},
			FinishReason: "length",
		},
	}}

	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test"}
	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if len(exec.executed) != 0 {
		t.Errorf("expected truncated tool calls never executed, got %v", exec.executed)
	}
	if result.FinishReason != HandoffReasonMaxTokens {
		t.Errorf("expected FinishReason=%q, got %q", HandoffReasonMaxTokens, result.FinishReason)
	}
}

// TestSubAgentTruncation_RecoversOnNextTurn: a single truncation followed by
// a normal completion must end cleanly (the "继续" turn resumes the model).
func TestSubAgentTruncation_RecoversOnNextTurn(t *testing.T) {
	model := &stubSeqModel{
		responses: []ModelResponse{
			{Message: ModelMessage{Role: "assistant", Content: "未完"}, FinishReason: "length"},
			{Message: ModelMessage{Role: "assistant", Content: "任务完成，测试通过。"}, FinishReason: "stop"},
		},
		classifierResp: `{"conclusion": true}`,
	}

	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}
	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "修复", MaxIterations: 5})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Errorf("expected FinishReason=%q after recovery, got %q", HandoffReasonCompleted, result.FinishReason)
	}
	if !strings.Contains(result.Summary, "任务完成") {
		t.Errorf("expected recovered summary, got %q", result.Summary)
	}
	if model.calls != 2 {
		t.Errorf("expected 2 calls (truncation + completion), got %d", model.calls)
	}
}

// TestSubAgentRunLoop_MaxIterationsReason: hitting the iteration cap must be
// reported as reason="max_iterations" (with TimedOut=true), not "completed".
func TestSubAgentRunLoop_MaxIterationsReason(t *testing.T) {
	exec := &recordingToolExecutor{}
	model := &stubSeqModel{responses: []ModelResponse{
		{
			Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID: "c1", Type: "function",
				Function: ModelFunctionCall{Name: "bash", Arguments: `{"command":"go build ./..."}`},
			}}},
			FinishReason: "tool_calls",
		},
	}}

	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test"}
	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 3})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if !result.TimedOut {
		t.Errorf("expected TimedOut=true at max iterations, got false")
	}
	if result.FinishReason != HandoffReasonMaxIterations {
		t.Errorf("expected FinishReason=%q, got %q", HandoffReasonMaxIterations, result.FinishReason)
	}
	if len(exec.executed) != 3 {
		t.Errorf("expected 3 executed tool turns, got %v", exec.executed)
	}
}

// TestSubAgentRunLoop_CancelledReason: a cancelled run reports
// reason="cancelled".
func TestSubAgentRunLoop_CancelledReason(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", Content: "开始分析"}, FinishReason: "stop"},
	}}
	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := runner.Run(ctx, Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonCancelled {
		t.Errorf("expected FinishReason=%q, got %q", HandoffReasonCancelled, result.FinishReason)
	}
	if !result.Blocked || result.BlockedBy != "cancelled" {
		t.Errorf("expected Blocked=true/cancelled, got blocked=%v by=%q", result.Blocked, result.BlockedBy)
	}
}

// TestSubAgentStructured_SubmitsResult: with StructuredResult the loop
// injects submit_result and terminates on a valid submission, capturing the
// summary and conclusions without touching the ConclusionClassifier.
func TestSubAgentStructured_SubmitsResult(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{
			Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID: "s1", Type: "function",
				Function: ModelFunctionCall{Name: SubmitResultToolName,
					Arguments: `{"summary":"发现三处问题","conclusions":["a.go:10","b.go:20"]}`},
			}}},
			FinishReason: "tool_calls",
		},
	}}

	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}
	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "审查", MaxIterations: 5, StructuredResult: true,
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if model.calls != 1 {
		t.Errorf("expected exactly 1 call (terminate on submit_result), got %d", model.calls)
	}
	if model.classifierCalls != 0 {
		t.Errorf("expected classifier never probed in structured mode, got %d probe(s)", model.classifierCalls)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Errorf("expected FinishReason=%q, got %q", HandoffReasonCompleted, result.FinishReason)
	}
	if result.Summary != "发现三处问题" {
		t.Errorf("expected Summary from submit_result params, got %q", result.Summary)
	}
	if len(result.Conclusions) != 2 || result.Conclusions[0] != "a.go:10" {
		t.Errorf("expected Conclusions parsed from params, got %v", result.Conclusions)
	}
	if !toolsContain(model.lastReq.Tools, SubmitResultToolName) {
		t.Errorf("expected submit_result in the request tools, got %+v", model.lastReq.Tools)
	}
}

// TestSubAgentStructured_TextOnlyNeverCompletes: in structured mode a
// text-only reply is narration until submit_result is called — the
// classifier is never probed and the nudge names the tool. After 3 strikes
// the run ends with reason="no_result" (partial answer preserved).
func TestSubAgentStructured_TextOnlyNeverCompletes(t *testing.T) {
	model := &stubSeqModel{
		responses: []ModelResponse{
			{Message: ModelMessage{Role: "assistant", Content: "我要开始分析了。"}, FinishReason: "stop"},
		},
		classifierResp: `{"conclusion": true}`,
	}

	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}
	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "审查", MaxIterations: 8, StructuredResult: true,
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if model.calls != 3 {
		t.Errorf("expected 3 calls (3-strike), got %d", model.calls)
	}
	if model.classifierCalls != 0 {
		t.Errorf("expected classifier never probed in structured mode, got %d probe(s)", model.classifierCalls)
	}
	if result.FinishReason != HandoffReasonNoResult {
		t.Errorf("expected FinishReason=%q, got %q", HandoffReasonNoResult, result.FinishReason)
	}
	if !strings.Contains(result.Summary, "开始分析") {
		t.Errorf("expected partial answer preserved, got %q", result.Summary)
	}
	if model.lastReq.Messages[len(model.lastReq.Messages)-1].Content != "" &&
		!strings.Contains(model.lastReq.Messages[len(model.lastReq.Messages)-1].Content, "submit_result") {
		t.Errorf("expected the nudge to name submit_result, last message=%q",
			model.lastReq.Messages[len(model.lastReq.Messages)-1].Content)
	}
}

// TestSubAgentStructured_InvalidSubmitRetries: an invalid submission (empty
// summary) is rejected as a tool error and the loop keeps running until a
// valid submission lands.
func TestSubAgentStructured_InvalidSubmitRetries(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{
			Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID: "s1", Type: "function",
				Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":""}`},
			}}},
			FinishReason: "tool_calls",
		},
		{
			Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID: "s2", Type: "function",
				Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"ok result"}`},
			}}},
			FinishReason: "tool_calls",
		},
	}}

	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}
	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "审查", MaxIterations: 5, StructuredResult: true,
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if model.calls != 2 {
		t.Errorf("expected 2 calls (invalid then valid), got %d", model.calls)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Errorf("expected FinishReason=%q after valid submission, got %q", HandoffReasonCompleted, result.FinishReason)
	}
	if result.Summary != "ok result" {
		t.Errorf("expected Summary from the valid submission, got %q", result.Summary)
	}
}

// TestFormatHandoffResult_ReasonAwareHeading: the tool digest a sub-agent
// handoff injects into the parent's history must name how the run ended
// instead of always claiming "Agent completed".
func TestFormatHandoffResult_ReasonAwareHeading(t *testing.T) {
	cases := []struct {
		name        string
		reason      string
		wantEnglish string // substring the English digest must contain
	}{
		{"completed", HandoffReasonCompleted, "Agent completed"},
		{"max_tokens", HandoffReasonMaxTokens, "token limit"},
		{"no_result", HandoffReasonNoResult, "without submitting"},
		{"stalled_narration", HandoffReasonStalledNarration, "narrating"},
		{"max_iterations", HandoffReasonMaxIterations, "turn limit"},
		{"loop_detected", HandoffReasonLoopDetected, "repeating"},
		{"cancelled", HandoffReasonCancelled, "cancelled"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			digest := formatHandoffResult(&HandoffResult{Summary: "s", FinishReason: tt.reason}, false)
			if !strings.Contains(digest, tt.wantEnglish) {
				t.Errorf("digest for %q = %q, want substring %q", tt.reason, digest, tt.wantEnglish)
			}
			if tt.reason != HandoffReasonCompleted && strings.Contains(digest, "Agent completed") {
				t.Errorf("digest for %q must not claim completion: %q", tt.reason, digest)
			}
		})
	}
}

// TestExecuteTurn_LengthTruncationDropsToolCalls: the main engine must never
// execute a tool call from a response cut off at the output cap; the turn
// resumes with a pinned "继续" instead.
func TestExecuteTurn_LengthTruncationDropsToolCalls(t *testing.T) {
	exec := &recordingToolExecutor{}
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{ToolCalls: []ModelToolCall{{
				ID: "c1", Type: "function",
				Function: ModelFunctionCall{Name: "bash", Arguments: `{"command":"rm -rf"}`},
			}}},
			{FinishReason: "length"},
		}},
		context:   &stubContextBuilder{},
		tools:     exec,
		state:     &TaskState{TurnNumber: 0},
		history:   []Message{{Role: "user", Content: "执行方案"}},
		config:    EngineConfig{ModelName: "test-model"},
		isChinese: true,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if len(exec.executed) != 0 {
		t.Errorf("expected truncated tool calls never executed, got %v", exec.executed)
	}
	if result.Done {
		t.Errorf("expected Done=false (truncated turn resumes), got Done=true")
	}
	if len(e.pendingPinnedMessages) != 1 || e.pendingPinnedMessages[0] != "继续" {
		t.Errorf("expected pinned \"继续\" to resume the turn, got %v", e.pendingPinnedMessages)
	}
}

// toolsContain reports whether the request carried the named tool spec.
func toolsContain(tools []ModelTool, name string) bool {
	for _, t := range tools {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}

// TestSpecialistAgent_CriticStructuredSubmitVerdict: critic 走结构化后，必须
// 通过 submit_result 提交含 VERDICT 的结论，父代理可从 digest 解析出 FAIL。
func TestSpecialistAgent_CriticStructuredSubmitVerdict(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{
			Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID: "s1", Type: "function",
				Function: ModelFunctionCall{Name: SubmitResultToolName,
					Arguments: `{"summary":"发现两处问题\n\nVERDICT: FAIL","conclusions":["a.go:10"]}`},
			}}},
			FinishReason: "tool_calls",
		},
	}}
	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}
	agent := &specialistAgent{
		id:       AgentCritic,
		spec:     AgentSpec{ID: AgentCritic, ToolNames: []string{"read", "grep", "glob", "lsp"}, StructuredResult: true},
		promptEn: criticPromptEn,
		promptZh: criticPromptZh,
		runner:   runner,
	}
	result, err := agent.Run(context.Background(), Handoff{Agent: AgentCritic, Goal: "验证实现", MaxIterations: 5})
	if err != nil {
		t.Fatalf("specialistAgent.Run error: %v", err)
	}
	if model.calls != 1 {
		t.Errorf("expected 1 call (terminate on submit_result), got %d", model.calls)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Errorf("expected FinishReason=completed, got %q", result.FinishReason)
	}
	if !strings.Contains(result.Summary, "VERDICT: FAIL") {
		t.Errorf("expected VERDICT: FAIL in summary, got %q", result.Summary)
	}
	if !toolsContain(model.lastReq.Tools, SubmitResultToolName) {
		t.Errorf("expected submit_result in tools for structured critic, got %+v", model.lastReq.Tools)
	}
}
