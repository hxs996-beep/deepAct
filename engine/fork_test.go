package engine

import (
	"context"
	"strings"
	"testing"
)

// forkHistory is the parent conversation shape the fork helpers are tested on:
// a complete first turn, then an in-flight second turn whose assistant message
// still carries THIS handoff call (no tool response yet).
func forkHistory() []Message {
	return []Message{
		{Role: "user", Content: "第一个请求"},
		{Role: "assistant", Content: "好的", ToolCalls: []MessageToolCall{{ID: "c1", Name: "read", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c1", Content: "文件内容"},
		{Role: "assistant", Content: "结论一"},
		{Role: "user", Content: "第二个请求"},
		{Role: "assistant", Content: "正在处理", ToolCalls: []MessageToolCall{{ID: "c2", Name: HandoffToolName, Arguments: "{}"}}},
	}
}

func TestForkablePrefix_EndsAtLastUserMessage(t *testing.T) {
	got := forkablePrefix(forkHistory())
	if len(got) != 5 {
		t.Fatalf("prefix len = %d, want 5 (through the second user message)", len(got))
	}
	if got[len(got)-1].Role != "user" {
		t.Fatalf("prefix must end on a user message, got %q", got[len(got)-1].Role)
	}
	// The in-flight handoff call has no tool response yet: forking it would
	// leave a dangling tool_call_id inside the sub-agent's history.
	for _, m := range got {
		for _, tc := range m.ToolCalls {
			if tc.ID == "c2" {
				t.Fatal("the in-flight handoff call must not be forked into the sub-agent")
			}
		}
	}
	if got := forkablePrefix(nil); got != nil {
		t.Errorf("no user message means nothing safe to fork, got %v", got)
	}
}

func TestLastNUserTurns(t *testing.T) {
	if got := lastNUserTurns(forkHistory(), 2); len(got) != 6 || got[0].Content != "第一个请求" {
		t.Fatalf("last 2 turns = %d msgs starting %q, want the whole history", len(got), got[0].Content)
	}
	if got := lastNUserTurns(forkHistory(), 1); len(got) != 2 || got[0].Content != "第二个请求" {
		t.Fatalf("last 1 turn = %+v, want to start at 第二个请求", got)
	}
	if got := lastNUserTurns(forkHistory(), 9); len(got) != len(forkHistory()) {
		t.Errorf("asking for more turns than exist must return everything")
	}
}

func TestForkHistoryFor_Specs(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.history = forkHistory()

	for _, spec := range []string{"", "none", "全部", "-1", "abc"} {
		if got := e.forkHistoryFor(spec); got != nil {
			t.Errorf("fork_turns=%q must not fork, got %v", spec, got)
		}
	}

	all := e.forkHistoryFor("all")
	if len(all) != 5 || all[0].Content != "第一个请求" {
		t.Fatalf(`fork_turns="all" = %d msgs, want 5 starting at the first request`, len(all))
	}
	// Messages become ModelMessages with their tool calls preserved.
	if len(all[1].ToolCalls) != 1 || all[1].ToolCalls[0].Function.Name != "read" {
		t.Errorf("forked tool call was not converted: %+v", all[1].ToolCalls)
	}
	if all[2].Role != "tool" || all[2].ToolCallID != "c1" {
		t.Errorf("forked tool response lost its id: %+v", all[2])
	}

	one := e.forkHistoryFor("1")
	if len(one) != 1 || one[0].Content != "第二个请求" {
		t.Fatalf(`fork_turns="1" = %+v, want just the last user request`, one)
	}
}

// TestRunLoopFrom_InjectsForkBeforeVolatileGoal pins the injection point: the
// forked prefix sits right after the system prompt, and the volatile goal is the
// LAST user message (highest recency).
func TestRunLoopFrom_InjectsForkBeforeVolatileGoal(t *testing.T) {
	model := &stubCompleteModel{resp: "done"}
	r := NewSubAgentRunner(model, stubToolExecutor{}, nil, "deepact-chat")
	r.workDir = t.TempDir()

	if _, err := r.runLoop(context.Background(), Handoff{
		Agent:         AgentSub,
		Goal:          "总结上面的讨论",
		TextResult:    true,
		ForkedHistory: []ModelMessage{{Role: "user", Content: "第一个请求"}, {Role: "assistant", Content: "结论一"}},
	}, "", 3); err != nil {
		t.Fatalf("runLoop: %v", err)
	}

	msgs := model.last.Messages
	if len(msgs) < 4 {
		t.Fatalf("request has %d messages, want system + fork + goal", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content == "" {
		t.Fatalf("first message must be the system prompt, got %q", msgs[0].Role)
	}
	if msgs[1].Content != "第一个请求" || msgs[2].Content != "结论一" {
		t.Fatalf("forked history must follow the system prompt, got %q / %q", msgs[1].Content, msgs[2].Content)
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "总结上面的讨论") {
		t.Fatalf("the volatile goal must be the last user message, got %q (%s)", last.Content, last.Role)
	}
}

// TestForkHistoryWithoutUserMessageIsNil: a history with no user message has no
// safe cut point, so nothing is forked.
func TestForkHistoryWithoutUserMessageIsNil(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.history = []Message{{Role: "assistant", Content: "无用户消息"}}
	if got := e.forkHistoryFor("all"); got != nil {
		t.Fatalf(`fork_turns="all" with no user message = %v, want nil`, got)
	}
}

// handoffCaptureAgent records the Handoff it was given and returns immediately.
type handoffCaptureAgent struct {
	id  AgentID
	got chan Handoff
}

func (a *handoffCaptureAgent) ID() AgentID     { return a.id }
func (a *handoffCaptureAgent) Spec() AgentSpec { return AgentSpec{ID: a.id} }
func (a *handoffCaptureAgent) Run(_ context.Context, in Handoff) (*HandoffResult, error) {
	a.got <- in
	return &HandoffResult{Summary: "ok", FinishReason: HandoffReasonCompleted}, nil
}

// TestForkTurns_ReachTheRun pins the wiring on both delegation paths: the sync
// path goes through runHandoff, the async path builds its own Handoff — and
// fork_turns must not be silently ignored on either.
func TestForkTurns_ReachTheRun(t *testing.T) {
	newEngine := func() (*Engine, *handoffCaptureAgent) {
		a := &handoffCaptureAgent{id: "cap", got: make(chan Handoff, 1)}
		reg := NewAgentRegistry()
		reg.Register(a)
		e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
		e.initBackgroundTasks()
		e.history = forkHistory()
		return e, a
	}

	e, a := newEngine()
	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "cap", Goal: "g", ForkTurns: "all",
	}, 0, "中文"); err != nil {
		t.Fatalf("sync handoff: %v", err)
	}
	if got := <-a.got; len(got.ForkedHistory) != 5 {
		t.Errorf("sync handoff forked %d messages, want 5", len(got.ForkedHistory))
	}

	e, a = newEngine()
	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "cap", Goal: "g", ForkTurns: "all", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("async handoff: %v", err)
	}
	if got := <-a.got; len(got.ForkedHistory) != 5 {
		t.Errorf("async handoff forked %d messages, want 5 (async must not skip the fork)", len(got.ForkedHistory))
	}
}

// TestForkTurns_NestedDelegationDoesNotFork: only the first level has the main
// history, so a nested delegation must leave ForkedHistory empty.
func TestForkTurns_NestedDelegationDoesNotFork(t *testing.T) {
	a := &handoffCaptureAgent{id: "cap", got: make(chan Handoff, 1)}
	reg := NewAgentRegistry()
	reg.Register(a)
	r := NewSubAgentRunner(&stubCompleteModel{resp: "x"}, stubToolExecutor{}, reg, "deepact-chat")

	if _, err := r.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "cap", Goal: "g", ForkTurns: "all",
	}, 1, "中文"); err != nil {
		t.Fatalf("nested handoff: %v", err)
	}
	if got := <-a.got; len(got.ForkedHistory) != 0 {
		t.Errorf("nested delegation forked %d messages, want none", len(got.ForkedHistory))
	}
}
