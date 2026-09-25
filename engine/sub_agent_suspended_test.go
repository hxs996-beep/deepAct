package engine

import (
	"context"
	"encoding/json"
	"testing"
)

// TestSuspendedCapturedOnLeafAskUser: the leaf path writes the ask_user
// placeholder before returning, so the captured breakpoint must point at that
// placeholder (resume REPLACES its content) and carry the cross-breakpoint
// counters.
func TestSuspendedCapturedOnLeafAskUser(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{
			Message: ModelMessage{Role: "assistant", Content: "已读 store.go。",
				ToolCalls: []ModelToolCall{{ID: "r1", Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"store.go"}`}}}},
			FinishReason: "tool_calls",
		},
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "ask1", Function: ModelFunctionCall{Name: AskUserToolName, Arguments: `{"question":"选哪个缓存？"}`}},
		}}},
	}}
	runner := budgetRunner(model, &recordingToolExecutor{})

	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5, UserLanguage: "中文"})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	s := result.Suspended
	if s == nil {
		t.Fatal("awaiting_user result must carry Suspended state")
	}
	if s.PendingToolID != "ask1" {
		t.Errorf("PendingToolID = %q, want ask1", s.PendingToolID)
	}
	if s.PendingToolMissing {
		t.Error("leaf path already wrote the placeholder; PendingToolMissing must be false")
	}
	if s.Iter != 1 {
		t.Errorf("Iter = %d, want 1 (the ask_user turn) — resume must not reset the iteration budget", s.Iter)
	}
	if s.ModelName != "test" {
		t.Errorf("ModelName = %q, want test", s.ModelName)
	}
	if s.Model == nil {
		t.Error("Model must carry the run's (forked) client for reuse")
	}

	// I1 leaf fill: replace in place, message count unchanged.
	before := len(s.History)
	filled := fillPendingToolResponse(s, "Redis")
	if len(filled) != before {
		t.Fatalf("leaf fill must replace, not append: len %d -> %d", before, len(filled))
	}
	last := filled[len(filled)-1]
	if last.Role != "tool" || last.ToolCallID != "ask1" || last.Content != "Redis" {
		t.Errorf("filled tail = %+v, want tool ask1 with the answer", last)
	}
}

// TestSuspendedCapturedOnNestedBubble: the nested-bubble path returns BEFORE
// writing the handoff tool response, so its history tail is an orphan
// assistant(tool_calls) and resume must APPEND (I1).
func TestSuspendedCapturedOnNestedBubble(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "h1", Function: ModelFunctionCall{Name: HandoffToolName, Arguments: `{"agent":"sub","goal":"子任务"}`}},
		}}},
	}}
	exec := &questionExecutor{questions: []string{"数据库连接串是什么？"}}
	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test"}

	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	s := result.Suspended
	if s == nil {
		t.Fatal("nested awaiting_user result must carry Suspended state")
	}
	if !s.PendingToolMissing {
		t.Error("nested bubble returns BEFORE writing the tool response; PendingToolMissing must be true")
	}
	if s.PendingToolID != "h1" {
		t.Errorf("PendingToolID = %q, want h1 (the unanswered handoff call)", s.PendingToolID)
	}
	if s.Iter != 0 {
		t.Errorf("Iter = %d, want 0", s.Iter)
	}
	if s.ChildRunID != "" {
		t.Errorf("ChildRunID = %q; the child handle only exists once runHandoff registers it", s.ChildRunID)
	}

	before := len(s.History)
	filled := fillPendingToolResponse(s, "postgres://x")
	if len(filled) != before+1 {
		t.Fatalf("nested fill must append: len %d -> %d", before, len(filled))
	}
	last := filled[len(filled)-1]
	if last.Role != "tool" || last.ToolCallID != "h1" || last.Content != "postgres://x" {
		t.Errorf("appended tail = %+v, want tool h1 with the answer", last)
	}
	// Adjacency: the appended response must directly follow its requesting
	// assistant(tool_calls) message.
	prev := filled[len(filled)-2]
	if prev.Role != "assistant" || len(prev.ToolCalls) == 0 {
		t.Errorf("tool response must immediately follow its assistant(tool_calls); got %+v", prev)
	}
}

// readThenQuestionExecutor serves read normally and handoff_to_agent with
// questions — the shape of one assistant emitting [read, handoff_to_agent].
type readThenQuestionExecutor struct{ questions []string }

func (x *readThenQuestionExecutor) Execute(_ ToolExecContext, calls []ToolCallRequest) []ToolResult {
	out := make([]ToolResult, 0, len(calls))
	for _, c := range calls {
		if c.Name == HandoffToolName {
			out = append(out, ToolResult{ToolCallID: c.ID, ToolName: c.Name, Status: "ok", Digest: "child asked", Questions: x.questions})
			continue
		}
		out = append(out, ToolResult{ToolCallID: c.ID, ToolName: c.Name, Status: "ok", Digest: "ok"})
	}
	return out
}

func (x *readThenQuestionExecutor) Specs() []ModelTool {
	return []ModelTool{{Type: "function", Function: ModelToolFunction{Name: "read"}}}
}

// TestSuspendedNestedWithSiblingToolCall: with [read, handoff_to_agent] in one
// assistant message the read response is already the history tail — the fill
// must be keyed on PendingToolID and APPEND, never replace the tail by position.
func TestSuspendedNestedWithSiblingToolCall(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "r1", Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"x.go"}`}},
			{ID: "h1", Function: ModelFunctionCall{Name: HandoffToolName, Arguments: `{"agent":"sub","goal":"g"}`}},
		}}},
	}}
	exec := &readThenQuestionExecutor{questions: []string{"选哪个？"}}
	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test"}

	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	s := result.Suspended
	if s == nil || !s.PendingToolMissing || s.PendingToolID != "h1" {
		t.Fatalf("want pending h1 missing, got %+v", s)
	}
	if tail := s.History[len(s.History)-1]; tail.Role != "tool" || tail.ToolCallID != "r1" {
		t.Fatalf("precondition: tail must be the sibling read response, got %+v", tail)
	}

	filled := fillPendingToolResponse(s, "Redis")
	if got := filled[len(filled)-1]; got.Role != "tool" || got.ToolCallID != "h1" || got.Content != "Redis" {
		t.Errorf("must APPEND the handoff response, got tail %+v", got)
	}
	for _, m := range filled {
		if m.Role == "tool" && m.ToolCallID == "r1" && m.Content != "ok" {
			t.Errorf("sibling read response must be untouched, got %+v", m)
		}
	}
}

// partitionProbeModel observes the prefix-cache partition a run is bound to: it
// records every ForkWithBaseURL URL, which happens exactly once per FRESH run
// (a resumed run must reuse the client and never fork again).
type partitionProbeModel struct {
	stubSeqModel
	urls []string
}

func (m *partitionProbeModel) Fork() ModelClient { return m }

func (m *partitionProbeModel) ForkWithBaseURL(url string) ModelClient {
	m.urls = append(m.urls, url)
	return m
}

// TestAgentResumeContinuesSameRun: resuming must continue the SAME run — the
// original history (no rebuild), the carried budget (no reset), and the
// original prefix-cache partition (no second fork).
func TestAgentResumeContinuesSameRun(t *testing.T) {
	model := &partitionProbeModel{stubSeqModel: stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "ask1", Function: ModelFunctionCall{Name: AskUserToolName, Arguments: `{"question":"选哪个缓存？"}`}},
		}}},
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "s1", Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"用 Redis。"}`}},
		}}},
	}}}
	runner := budgetRunner(model, readSpecExec())
	runner.SetSubAgentTokenBudget(1000)
	runner.SetSubAgentPartitionURL(func(p string) string { return "http://example.invalid/" + p })

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研", StructuredResult: true, UserLanguage: "中文",
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	s := result.Suspended
	if s == nil {
		t.Fatal("expected a suspension from the ask_user turn")
	}
	if len(model.urls) != 1 {
		t.Fatalf("a fresh run forks once onto its partition, got %v", model.urls)
	}
	if s.Partition == "" {
		t.Fatal("the fresh run must record its partition name for reuse")
	}
	freshURL := model.urls[0]
	// Pretend the run had already burned most of its budget before asking.
	s.Spent = ModelUsage{CacheMissTokens: 900, TotalTokens: 900}

	resumed, err := runner.runLoopResume(context.Background(), s, "Redis")
	if err != nil {
		t.Fatalf("runLoopResume error: %v", err)
	}
	if resumed.FinishReason != HandoffReasonCompleted {
		t.Fatalf("resumed run must complete via submit_result, got %q", resumed.FinishReason)
	}
	if len(model.urls) != 1 || model.urls[0] != freshURL {
		t.Errorf("resume must reuse the original partition URL (I2); got %v, want [%s]", model.urls, freshURL)
	}
	if resumed.Usage == nil || resumed.Usage.CacheMissTokens < 900 {
		t.Errorf("resumed usage must continue from the breakpoint, got %+v", resumed.Usage)
	}

	// The resumed request continues the SAME history: it still starts with the
	// original system message, and the ask_user placeholder now carries the
	// answer (I1).
	msgs := model.lastReq.Messages
	if len(msgs) == 0 || msgs[0].Role != "system" {
		t.Fatalf("resumed history must start with the original system message, got %d messages", len(msgs))
	}
	found := false
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID == "ask1" && m.Content == "Redis" {
			found = true
		}
	}
	if !found {
		t.Errorf("the ask_user tool message must carry the user's answer; history=%+v", msgs)
	}
	if resumed.Suspended != nil {
		t.Error("the resumed run should not suspend again")
	}
}

// TestFillPendingRefusesOrphanedInsert: never fabricate a tool response — if
// the history does not actually contain the pending call (or the placeholder to
// replace), the history is returned unchanged.
func TestFillPendingRefusesOrphanedInsert(t *testing.T) {
	// Unknown tool_call_id → no assistant requested it.
	unknown := &SuspendedRun{PendingToolID: "x", PendingToolMissing: true,
		History: []ModelMessage{{Role: "assistant", Content: "纯文本"}}}
	if out := fillPendingToolResponse(unknown, "a"); len(out) != len(unknown.History) {
		t.Errorf("must not append a response for an unknown tool_call_id: %+v", out)
	}

	// Placeholder expected but absent → nothing to replace.
	noPlaceholder := &SuspendedRun{PendingToolID: "y", PendingToolMissing: false,
		History: []ModelMessage{{Role: "assistant", ToolCalls: []ModelToolCall{{ID: "y"}}}}}
	if out := fillPendingToolResponse(noPlaceholder, "a"); len(out) != len(noPlaceholder.History) {
		t.Errorf("no placeholder to replace; must refuse: %+v", out)
	}

	// Empty PendingToolID → untouched.
	empty := &SuspendedRun{History: []ModelMessage{{Role: "user", Content: "u"}}}
	if out := fillPendingToolResponse(empty, "a"); len(out) != 1 || out[0].Content != "u" {
		t.Errorf("empty PendingToolID must be a no-op: %+v", out)
	}
}

// TestRunHandoffRegistersSuspension: runHandoff must hand a suspension to the
// injected registrar and put the returned handle into ToolResult.RunID — before
// formatting the digest. Drives the REAL runHandoff (only the registrar is
// fake): a stubbed RunID would pass while the real path stays broken.
func TestRunHandoffRegistersSuspension(t *testing.T) {
	suspended := &SuspendedRun{Input: Handoff{Goal: "g"}}
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "选哪个？", Questions: []string{"选哪个？"},
		FinishReason: HandoffReasonAwaitingUser, Suspended: suspended,
	}})

	var got *SuspendedRun
	res := runHandoff(context.Background(),
		ToolCallRequest{ID: "h1", Name: HandoffToolName, Input: json.RawMessage(`{"agent":"sub","goal":"g"}`)},
		handoffOptions{
			resolve:  func(id AgentID) (Agent, error) { return reg.Get(id) },
			depth:    1,
			register: func(s *SuspendedRun, a AgentID, goal string) string { got = s; return "bg-7" },
		})
	if got != suspended {
		t.Fatal("runHandoff must hand the suspension to the registrar")
	}
	if res.RunID != "bg-7" {
		t.Errorf("ToolResult.RunID = %q, want bg-7", res.RunID)
	}
}

// TestSubAgentRunnerWiresRegistrar: the nested backend must pass the runner's
// registrar into runHandoff. This is the wiring whose absence left depth>=1
// suspensions unregisterable (SubAgentRunner holds no *Engine reference).
func TestSubAgentRunnerWiresRegistrar(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "选哪个？", Questions: []string{"选哪个？"},
		FinishReason: HandoffReasonAwaitingUser, Suspended: &SuspendedRun{Input: Handoff{Goal: "g"}},
	}})
	runner := &SubAgentRunner{registry: reg, modelName: "test"}
	called := false
	runner.SetSuspendedRegistrar(func(s *SuspendedRun, a AgentID, goal string) string {
		called = true
		return "bg-9"
	})

	res, err := runner.RunSubAgent(context.Background(), HandoffToAgentParams{Agent: "sub", Goal: "g"}, 1, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if !called {
		t.Fatal("SubAgentRunner.RunSubAgent must pass its registrar to runHandoff")
	}
	if res.RunID != "bg-9" {
		t.Errorf("RunID = %q, want bg-9", res.RunID)
	}
}

// TestSubAgentRunnerWithoutRegistrarKeepsTodayBehaviour: with no registrar
// injected (bare runner, embeddings) a nested suspension must simply not
// register — no panic, empty handle.
func TestSubAgentRunnerWithoutRegistrarKeepsTodayBehaviour(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "q", Questions: []string{"q"},
		FinishReason: HandoffReasonAwaitingUser, Suspended: &SuspendedRun{Input: Handoff{Goal: "g"}},
	}})
	runner := &SubAgentRunner{registry: reg, modelName: "test"}
	res, err := runner.RunSubAgent(context.Background(), HandoffToAgentParams{Agent: "sub", Goal: "g"}, 1, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.RunID != "" {
		t.Errorf("RunID = %q, want empty (no registrar injected)", res.RunID)
	}
	if res.FinishReason != HandoffReasonAwaitingUser {
		t.Errorf("FinishReason = %q, want awaiting_user", res.FinishReason)
	}
}
