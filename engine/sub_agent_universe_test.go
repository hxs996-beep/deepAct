package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// Universe tests for the sub-agent read-only tool model. The execution gate
// is the security-bearing half of the design: tools.Executor resolves any
// registered name by lookup (it never checks the offered list), so an
// out-of-set call — hallucinated or injected — must be refused inside
// runLoop. Visible-set filtering alone is not a security boundary. The async
// dispatch path (Engine.dispatchAsync) drives the same agent.Run → runLoop,
// so the gate covers it constructively.

// scriptToolExecutor offers an arbitrary spec list and records executed
// names, so each test controls exactly what the shared registry contains.
type scriptToolExecutor struct {
	specs    []ModelTool
	executed []string
}

func (s *scriptToolExecutor) Execute(_ ToolExecContext, calls []ToolCallRequest) []ToolResult {
	out := make([]ToolResult, 0, len(calls))
	for _, c := range calls {
		s.executed = append(s.executed, c.Name)
		out = append(out, ToolResult{ToolCallID: c.ID, ToolName: c.Name, Status: "ok", Digest: "ok"})
	}
	return out
}

func (s *scriptToolExecutor) Specs() []ModelTool { return s.specs }

func specNamed(name string) ModelTool {
	return ModelTool{Type: "function", Function: ModelToolFunction{Name: name}}
}

func namesOf(tools []ModelTool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Function.Name)
	}
	return out
}

func hasName(tools []ModelTool, name string) bool {
	for _, t := range tools {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}

// TestSubAgentUniverse_SubOfferedTools: with no tools override, a sub run
// offers exactly the universe members present in the registry plus the
// channels. bash/write/edit/revert and any non-universe spec never appear.
func TestSubAgentUniverse_SubOfferedTools(t *testing.T) {
	exec := &scriptToolExecutor{specs: []ModelTool{
		specNamed("read"), specNamed("grep"), specNamed("bash"), specNamed("write"),
		specNamed("edit"), specNamed("revert"), specNamed("github_search"),
	}}
	model := &stubSeqModel{responses: []ModelResponse{{
		Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID: "s", Type: "function",
			Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"done"}`},
		}}},
		FinishReason: "tool_calls",
	}}}
	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test"}

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "g", StructuredResult: true,
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Fatalf("expected completion, got %q", result.FinishReason)
	}
	offered := model.lastReq.Tools
	for _, want := range []string{"read", "grep", HandoffToolName, AskUserToolName, SubmitResultToolName} {
		if !hasName(offered, want) {
			t.Errorf("offered tools must contain %q, got %v", want, namesOf(offered))
		}
	}
	for _, banned := range []string{"bash", "write", "edit", "revert", "github_search"} {
		if hasName(offered, banned) {
			t.Errorf("offered tools must not contain %q, got %v", banned, namesOf(offered))
		}
	}
}

// TestSubAgentUniverse_CallerOverrideFailsLoud: a delegating model passing
// write-class tools gets a loud, educational error — not silent filtering.
func TestSubAgentUniverse_CallerOverrideFailsLoud(t *testing.T) {
	capture := &captureSubRunner{}
	a := &specSubAgent{runner: capture, spec: subSpec()}
	_, err := a.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "g", Tools: []string{"bash", "write"},
	})
	if err == nil {
		t.Fatal("expected an error for write-class tools, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"bash", "write", "read-only"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must name %q for self-correction, got %q", want, msg)
		}
	}
}

// TestSubAgentUniverse_CallerMayNarrow: an in-universe override narrows the
// offered set; channels stay.
func TestSubAgentUniverse_CallerMayNarrow(t *testing.T) {
	exec := &scriptToolExecutor{specs: []ModelTool{specNamed("read"), specNamed("grep"), specNamed("glob")}}
	model := &stubSeqModel{responses: []ModelResponse{{
		Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID: "s", Type: "function",
			Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"done"}`},
		}}},
		FinishReason: "tool_calls",
	}}}
	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test"}

	_, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "g", Tools: []string{"grep", "glob"}, StructuredResult: true,
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	offered := namesOf(model.lastReq.Tools)
	for _, want := range []string{"grep", "glob", HandoffToolName, AskUserToolName, SubmitResultToolName} {
		if !hasName(model.lastReq.Tools, want) {
			t.Errorf("narrowed offer must contain %q, got %v", want, offered)
		}
	}
	if hasName(model.lastReq.Tools, "read") {
		t.Errorf("narrowed offer must drop out-of-list tools, got %v", offered)
	}
}

// TestSubAgentUniverse_ResearcherDefaultUnchanged: researcher's default
// read-only set is unaffected by the universe change.
func TestSubAgentUniverse_ResearcherDefaultUnchanged(t *testing.T) {
	capture := &captureSubRunner{}
	a := &specSubAgent{runner: capture, spec: researcherSpec()}
	if _, err := a.Run(context.Background(), Handoff{Agent: AgentResearcher, Goal: "g"}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	want := []string{"read", "read_multi", "grep", "glob", "lsp"}
	if len(capture.input.Tools) != len(want) {
		t.Fatalf("expected researcher default %v, got %v", want, capture.input.Tools)
	}
	for i, n := range want {
		if capture.input.Tools[i] != n {
			t.Errorf("researcher default[%d] = %q, want %q", i, capture.input.Tools[i], n)
		}
	}
}

// TestSubAgentExecutionGate_RefusesOutOfSetCall: the registry contains bash
// but the run never offered it; a hallucinated bash call must be refused
// without executing, and the run must survive to normal completion.
func TestSubAgentExecutionGate_RefusesOutOfSetCall(t *testing.T) {
	exec := &scriptToolExecutor{specs: []ModelTool{specNamed("read"), specNamed("bash")}}
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID: "b1", Type: "function",
			Function: ModelFunctionCall{Name: "bash", Arguments: `{"command":"rm -rf /"}`},
		}}}, FinishReason: "tool_calls"},
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID: "s", Type: "function",
			Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"done"}`},
		}}}, FinishReason: "tool_calls"},
	}}
	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test"}

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "g", StructuredResult: true,
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Fatalf("run must survive a refused call and complete, got %q", result.FinishReason)
	}
	for _, name := range exec.executed {
		if name == "bash" {
			t.Fatalf("bash must never execute, executed: %v", exec.executed)
		}
	}
	// The refusal message must reach the model so it can self-correct.
	blockedSeen := false
	for _, msg := range model.lastReq.Messages {
		if strings.Contains(msg.Content, "Blocked: bash") {
			blockedSeen = true
		}
	}
	if !blockedSeen {
		t.Errorf("the blocked tool message must reach the next request, messages: %v", model.lastReq.Messages)
	}
}

// TestSubAgentExecutionGate_ThreeStrikesEndsRun: three consecutive refused
// calls with no real dispatch in between end the run with loop_detected; a
// real dispatch in between resets the streak.
func TestSubAgentExecutionGate_ThreeStrikesEndsRun(t *testing.T) {
	exec := &scriptToolExecutor{specs: []ModelTool{specNamed("read"), specNamed("bash")}}
	bashTurn := ModelResponse{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
		ID: "b", Type: "function",
		Function: ModelFunctionCall{Name: "bash", Arguments: `{"command":"x"}`},
	}}}, FinishReason: "tool_calls"}
	readTurn := ModelResponse{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
		ID: "r", Type: "function",
		Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"x.go"}`},
	}}}, FinishReason: "tool_calls"}

	// Pure streak: blocked, blocked, blocked → loop_detected on the 3rd.
	model := &stubSeqModel{responses: []ModelResponse{bashTurn}}
	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test"}
	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g"})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonLoopDetected {
		t.Fatalf("expected loop_detected after 3 consecutive refusals, got %q", result.FinishReason)
	}
	if model.calls != 3 {
		t.Errorf("expected the run to end at the 3rd refused call, got %d calls", model.calls)
	}

	// A real dispatch resets the streak: blocked, dispatched, then blocked
	// twice — still under the threshold, the run keeps going.
	exec2 := &scriptToolExecutor{specs: []ModelTool{specNamed("read"), specNamed("bash")}}
	model2 := &stubSeqModel{responses: []ModelResponse{bashTurn, readTurn, bashTurn, readTurn}}
	runner2 := &SubAgentRunner{model: model2, tools: exec2, modelName: "test"}
	result2, err := runner2.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 6})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result2.FinishReason == HandoffReasonLoopDetected {
		t.Error("an interleaved real dispatch must reset the blocked streak, got loop_detected")
	}
	if len(exec2.executed) != 3 {
		t.Errorf("expected 3 dispatched read calls (turns 2/4/6), got %v", exec2.executed)
	}
}

// TestSubAgentUniverse_NestedRunSameGate: the gate is active at depth > 0 —
// a grandchild cannot run bash either.
func TestSubAgentUniverse_NestedRunSameGate(t *testing.T) {
	exec := &scriptToolExecutor{specs: []ModelTool{specNamed("read"), specNamed("bash")}}
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID: "b1", Type: "function",
			Function: ModelFunctionCall{Name: "bash", Arguments: `{"command":"x"}`},
		}}}, FinishReason: "tool_calls"},
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID: "s", Type: "function",
			Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"done"}`},
		}}}, FinishReason: "tool_calls"},
	}}
	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test"}

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "g", Depth: 1, StructuredResult: true,
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Fatalf("nested run must complete, got %q", result.FinishReason)
	}
	for _, name := range exec.executed {
		if name == "bash" {
			t.Fatalf("bash must never execute at any depth, executed: %v", exec.executed)
		}
	}
}

// TestSubAgentUniverse_RoleScopeIntersection: a role-restricted agent accepts
// only narrowing within its own set — a request outside it is rejected with
// a distinct, actionable message.
func TestSubAgentUniverse_RoleScopeIntersection(t *testing.T) {
	capture := &captureSubRunner{}
	a := &specSubAgent{runner: capture, spec: researcherSpec()}

	// In-universe but outside the role's scope → role-scope error.
	_, err := a.Run(context.Background(), Handoff{Agent: AgentResearcher, Goal: "g", Tools: []string{"web_search"}})
	if err == nil || !strings.Contains(err.Error(), "researcher") {
		t.Fatalf("expected a role-scope error naming the role, got %v", err)
	}

	// Narrowing within the role's scope passes.
	if _, err := a.Run(context.Background(), Handoff{Agent: AgentResearcher, Goal: "g", Tools: []string{"grep"}}); err != nil {
		t.Fatalf("narrowing within the role must pass, got %v", err)
	}
	if len(capture.input.Tools) != 1 || capture.input.Tools[0] != "grep" {
		t.Errorf("expected narrowed [grep], got %v", capture.input.Tools)
	}
}

// TestSubAgentUniverse_MCPShapeExcluded: MCP tools register as
// "<server>_<tool>" and never enter the universe. A stub shaped like a real
// MCP tool is invisible regardless of the allowList. Colliding names
// (server "web" + tool "search" → "web_search") DO pass by name: a
// name-based allow-list cannot distinguish origin — a documented residual
// risk (an MCP server named to shadow a built-in is self-inflicted
// configuration, not an injection surface).
func TestSubAgentUniverse_MCPShapeExcluded(t *testing.T) {
	exec := &scriptToolExecutor{specs: []ModelTool{specNamed("read"), specNamed("github_search")}}
	model := &stubSeqModel{responses: []ModelResponse{{
		Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID: "s", Type: "function",
			Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"done"}`},
		}}},
		FinishReason: "tool_calls",
	}}}
	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test"}

	// Even an explicit allowList naming the MCP-shaped tool fails loud at
	// the spec layer before any run starts.
	capture := &captureSubRunner{}
	a := &specSubAgent{runner: capture, spec: subSpec()}
	if _, err := a.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", Tools: []string{"github_search"}}); err == nil {
		t.Fatal("an MCP-shaped tool must fail loud in the tools override")
	}

	if _, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "g", StructuredResult: true,
	}); err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if hasName(model.lastReq.Tools, "github_search") {
		t.Errorf("MCP-shaped tool must never be offered, got %v", namesOf(model.lastReq.Tools))
	}

	// Collision: a spec registered under a universe name is offered as that
	// name — origin is invisible to a name-based list.
	collision := &scriptToolExecutor{specs: []ModelTool{specNamed("web_search")}}
	model2 := &stubSeqModel{responses: []ModelResponse{{
		Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID: "s", Type: "function",
			Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"done"}`},
		}}},
		FinishReason: "tool_calls",
	}}}
	runner2 := &SubAgentRunner{model: model2, tools: collision, modelName: "test"}
	if _, err := runner2.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "g", StructuredResult: true,
	}); err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if !hasName(model2.lastReq.Tools, "web_search") {
		t.Error("a colliding name passes by name (documented residual risk)")
	}
}

// TestSubAgentUniverse_ConcurrentSpawns: parallel handoffs share one runner
// (as production parallel tool calls do); the runner's only mutable state is
// the partitionSeq atomic, so concurrent runs must be race-free and all
// gate-protected. The -race detector asserts the absence of data races.
func TestSubAgentUniverse_ConcurrentSpawns(t *testing.T) {
	exec := &scriptToolExecutor{specs: []ModelTool{specNamed("read"), specNamed("bash")}}
	model := &threadSafeSubmitModel{}
	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test"}

	const n = 8
	var wg sync.WaitGroup
	reasons := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := runner.Run(context.Background(), Handoff{
				Agent: AgentSub, Goal: "g", StructuredResult: true,
			})
			if err != nil {
				reasons[i] = "error: " + err.Error()
				return
			}
			reasons[i] = result.FinishReason
		}(i)
	}
	wg.Wait()
	for i, r := range reasons {
		if r != HandoffReasonCompleted {
			t.Errorf("run %d: expected %q, got %q", i, HandoffReasonCompleted, r)
		}
	}
}

// threadSafeSubmitModel always submits immediately; safe for concurrent use
// because every Run goroutine shares one model client, as production does.
type threadSafeSubmitModel struct{}

func (m *threadSafeSubmitModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *threadSafeSubmitModel) Complete(context.Context, ModelRequest) (*ModelResponse, error) {
	return &ModelResponse{
		Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID: "s", Type: "function",
			Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"done"}`},
		}}},
		FinishReason: "tool_calls",
	}, nil
}
