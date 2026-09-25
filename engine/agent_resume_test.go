package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// suspendMockAgent returns a result that stops on ask_user.
func suspendMockAgent(summary string) *mockAgentForHandoff {
	return &mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary:      summary,
		Questions:    []string{summary},
		FinishReason: HandoffReasonAwaitingUser,
		Suspended:    &SuspendedRun{Input: Handoff{Goal: "g"}, ChildRunID: "bg-child"},
	}}
}

// TestSuspendRegistersJobAndBindsQuestion: a synchronous suspension becomes an
// awaiting_user job, and the bubbled question carries that job's handle so the
// answer can be routed back (agent_resume).
func TestSuspendRegistersJobAndBindsQuestion(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(suspendMockAgent("选哪个缓存？"))
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}

	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{Agent: "sub", Goal: "任务"}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.FinishReason != HandoffReasonAwaitingUser {
		t.Fatalf("FinishReason = %q, want awaiting_user", res.FinishReason)
	}
	if res.RunID == "" {
		t.Fatal("a synchronous suspension must be registered (handle in ToolResult.RunID)")
	}
	e.bgMu.Lock()
	task := e.bgTasks[res.RunID]
	e.bgMu.Unlock()
	if task == nil || task.state != bgStateAwaitingUser {
		t.Fatalf("no awaiting_user entry for %q (task=%+v)", res.RunID, task)
	}
	if task.result == nil {
		t.Error("registered suspension must create its result channel (I9)")
	}
	if task.childRunID != "bg-child" {
		t.Errorf("childRunID = %q, want bg-child", task.childRunID)
	}
	if req := e.peekAskUser(); req == nil || req.RunID != res.RunID {
		t.Errorf("the question must be bound to the job handle, got %+v", e.peekAskUser())
	}
}

// TestSuspendCapFailsLoud: when the suspension table is full the new question is
// NOT registered — fail-loud, back to today's behaviour (bubble + re-delegate),
// never a silent half-registered entry.
func TestSuspendCapFailsLoud(t *testing.T) {
	e := &Engine{state: &TaskState{}, config: EngineConfig{MaxSuspendedSubAgents: 1}}
	e.initBackgroundTasks()

	first := e.RegisterSuspended(&SuspendedRun{Input: Handoff{Goal: "g"}, ChildRunID: "bg-x"}, AgentSub, "g")
	if first == "" {
		t.Fatal("the first suspension must register")
	}
	second := e.RegisterSuspended(&SuspendedRun{Input: Handoff{Goal: "g2"}}, AgentSub, "g2")
	if second != "" {
		t.Errorf("cap=1 must refuse the second suspension, got %q", second)
	}
	if n := e.suspendedCount(); n != 1 {
		t.Errorf("suspendedCount = %d, want 1", n)
	}
	if n := e.inFlightCount(); n != 0 {
		t.Errorf("inFlightCount = %d, want 0 (suspensions hold no LLM slot)", n)
	}
}

// TestAsyncSuspendDoesNotStaleResult: an async job that stops on ask_user becomes
// an awaiting_user entry that keeps its job id — and NOTHING is written to its
// result channel (nor is agent_done emitted). Otherwise the resumed run's real
// result would hit select/default and be dropped, while agent_poll reported the
// stale question as "done".
func TestAsyncSuspendDoesNotStaleResult(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(suspendMockAgent("选哪个？"))
	var mu sync.Mutex
	var events []string
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{},
		config: EngineConfig{OnProgress: func(ev ProgressEvent) {
			mu.Lock()
			events = append(events, ev.Type)
			mu.Unlock()
		}}}

	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{Agent: "sub", Goal: "任务", Async: true}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "ok" || res.FinishReason != HandoffReasonAsyncRunning {
		t.Fatalf("dispatch must return immediately, got res=%+v", res)
	}

	task := waitTaskState(t, e, "bg-1", bgStateAwaitingUser)
	if len(task.result) != 0 {
		t.Error("a suspended job must not park a result in its channel (N2)")
	}
	if task.childRunID != "bg-child" {
		t.Errorf("childRunID = %q, want bg-child", task.childRunID)
	}
	mu.Lock()
	for _, ty := range events {
		if ty == "agent_done" {
			t.Errorf("agent_done must be withheld while suspended; events=%v", events)
		}
	}
	mu.Unlock()

	// I4: the suspended entry survives the Run-exit cleanup.
	e.cancelBackgroundTasks()
	if n := e.suspendedCount(); n != 1 {
		t.Errorf("suspended entry must survive cancelBackgroundTasks, got %d", n)
	}
}

// waitTaskState polls until the job reaches want, or fails the test.
func waitTaskState(t *testing.T, e *Engine, jobID, want string) *bgTask {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.bgMu.Lock()
		task := e.bgTasks[jobID]
		state := ""
		if task != nil {
			state = task.state
		}
		e.bgMu.Unlock()
		if state == want {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job %s never reached state %q", jobID, want)
	return nil
}

// TestMultiSlotQuestionsBothSurvive: questions from parallel sub-agents must all
// survive — the single slot used to silently overwrite them.
func TestMultiSlotQuestionsBothSurvive(t *testing.T) {
	e := &Engine{}
	e.pushAskUser(&AskUserRequest{Question: "第一个", RunID: "bg-1"})
	e.pushAskUser(&AskUserRequest{Question: "第二个", RunID: "bg-2"})

	if req := e.peekAskUser(); req == nil || req.Question != "第一个" {
		t.Fatalf("head = %+v, want 第一个", req)
	}
	if got := e.askUserOptions(); got != nil {
		t.Errorf("a question without options must present nil options, got %v", got)
	}
	e.consumeAskUser()
	if req := e.peekAskUser(); req == nil || req.Question != "第二个" || req.RunID != "bg-2" {
		t.Fatalf("after consuming the head, next = %+v, want 第二个/bg-2", req)
	}
	e.consumeAskUser()
	if req := e.peekAskUser(); req != nil {
		t.Errorf("queue must be empty, got %+v", req)
	}

	e.pushAskUser(&AskUserRequest{Question: "x"})
	e.clearAskUser()
	if req := e.peekAskUser(); req != nil {
		t.Errorf("clearAskUser must drop every question, got %+v", req)
	}
}

// TestSetHistoryClearsSuspended: replacing the history (/resume continues another
// session) must drop suspended jobs and their questions — they point at a history
// that no longer exists — while in-flight work of the current Run is untouched.
func TestSetHistoryClearsSuspended(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.initBackgroundTasks()
	h1 := e.RegisterSuspended(&SuspendedRun{Input: Handoff{Goal: "a"}}, AgentSub, "a")
	h2 := e.RegisterSuspended(&SuspendedRun{Input: Handoff{Goal: "b"}}, AgentSub, "b")
	if h1 == "" || h2 == "" {
		t.Fatal("setup: both suspensions must register")
	}
	e.pushAskUser(&AskUserRequest{Question: "q", RunID: h1})
	e.bgMu.Lock()
	e.bgTasks["bg-run"] = &bgTask{id: "bg-run", state: bgStateRunning}
	e.bgMu.Unlock()

	e.SetHistory([]Message{{Role: "user", Content: "恢复的历史"}})

	if n := e.suspendedCount(); n != 0 {
		t.Errorf("suspended entries must be dropped on history swap, got %d", n)
	}
	if req := e.peekAskUser(); req != nil {
		t.Errorf("their questions must be dropped too, got %+v", req)
	}
	if n := e.inFlightCount(); n != 1 {
		t.Errorf("running jobs of the current Run must survive, got %d", n)
	}
}

// ---- task 5: agent_resume -------------------------------------------------

// submitAfterResumeModel submits a structured result on its first call — the
// shape of a resumed run that had everything it needed once the user answered.
func submitAfterResumeModel(summary string) *stubSeqModel {
	return &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "s1", Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"` + summary + `"}`}},
		}}},
	}}
}

// resumeEngine builds an engine whose "sub" role is a real runner+specSubAgent,
// so the resume path can reach RunSuspended.
func resumeEngine(t *testing.T, runner *SubAgentRunner) *Engine {
	t.Helper()
	reg := NewAgentRegistry()
	reg.Register(&specSubAgent{runner: runner, spec: AgentSpec{ID: AgentSub, StructuredResult: true}})
	return &Engine{agents: reg, state: &TaskState{}}
}

// leafSuspended builds a leaf breakpoint: assistant(ask_user) + its placeholder.
func leafSuspended(t *testing.T, model ModelClient) *SuspendedRun {
	t.Helper()
	return &SuspendedRun{
		Question:      "选哪个缓存？",
		PendingToolID: "ask1",
		Input:         Handoff{Agent: AgentSub, Goal: "g", StructuredResult: true, MaxIterations: 5},
		Model:         model,
		History: []ModelMessage{
			{Role: "system", Content: "sys"},
			{Role: "assistant", ToolCalls: []ModelToolCall{{ID: "ask1", Type: "function", Function: ModelFunctionCall{Name: AskUserToolName}}}},
			{Role: "tool", ToolCallID: "ask1", Content: "✓ 已记录问题，等待用户回答。"},
		},
	}
}

// TestAgentResumeDrainsToLeaf: the model names the TOP handle, which is waiting
// for its child — the engine must deliver the answer to the LEAF (I8), never
// into the top's pending handoff slot.
func TestAgentResumeDrainsToLeaf(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.initBackgroundTasks()
	leaf := e.RegisterSuspended(leafSuspended(t, submitAfterResumeModel("用 Redis。")), AgentSub, "g")
	top := e.RegisterSuspended(&SuspendedRun{
		PendingToolID:      "h1",
		PendingToolMissing: true,
		ChildRunID:         leaf,
		Input:              Handoff{Agent: AgentSub, Goal: "g"},
	}, AgentSub, "g")

	leafID, s, err := e.takeLeafForResume(context.Background(), top)
	if err != nil {
		t.Fatalf("takeLeafForResume: %v", err)
	}
	if leafID != leaf {
		t.Errorf("drilled to %q, want the leaf %q", leafID, leaf)
	}
	if s.Question != "选哪个缓存？" {
		t.Errorf("leaf question = %q", s.Question)
	}
	// The leaf is claimed (running, no longer suspended); the top is untouched.
	e.bgMu.Lock()
	leafState, topState := e.bgTasks[leaf].state, e.bgTasks[top].state
	e.bgMu.Unlock()
	if leafState != bgStateRunning {
		t.Errorf("leaf state = %q, want running (claimed)", leafState)
	}
	if topState != bgStateAwaitingUser {
		t.Errorf("top state = %q, want awaiting_user (delivery slot)", topState)
	}
}

// TestAgentResumeFailLoud: unknown / running / already-claimed handles must be
// refused with a directive message, never silently rebuilt.
func TestAgentResumeFailLoud(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.initBackgroundTasks()
	if _, _, err := e.takeLeafForResume(context.Background(), "bg-404"); err == nil {
		t.Error("unknown handle must fail loud")
	}
	e.bgMu.Lock()
	e.bgTasks["bg-run"] = &bgTask{id: "bg-run", state: bgStateRunning, result: make(chan *HandoffResult, 1)}
	e.bgMu.Unlock()
	_, _, err := e.takeLeafForResume(context.Background(), "bg-run")
	if err == nil || !strings.Contains(err.Error(), "not waiting for user input") {
		t.Errorf("a running handle must be refused, got %v", err)
	}
	// A chain whose child is gone cannot be answered either.
	orphan := e.RegisterSuspended(&SuspendedRun{
		PendingToolID: "h1", PendingToolMissing: true, ChildRunID: "bg-gone",
		Input: Handoff{Agent: AgentSub, Goal: "g"},
	}, AgentSub, "g")
	if _, _, err := e.takeLeafForResume(context.Background(), orphan); err == nil {
		t.Error("a chain with a missing child must fail loud")
	}
}

// TestAgentResumeIterationBudgetNotReset: the resumed loop continues from the
// breakpoint's iteration, so a capped run does not get a fresh budget.
func TestAgentResumeIterationBudgetNotReset(t *testing.T) {
	read := ModelResponse{
		Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "r", Type: "function", Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"x.go"}`}},
		}},
		FinishReason: "tool_calls",
	}
	ask := ModelResponse{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
		{ID: "ask1", Type: "function", Function: ModelFunctionCall{Name: AskUserToolName, Arguments: `{"question":"q"}`}},
	}}}
	model := &stubSeqModel{responses: []ModelResponse{read, read, ask}}
	runner := budgetRunner(model, readSpecExec())

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "g", MaxIterations: 3, UserLanguage: "中文",
	})
	if err != nil || result.Suspended == nil {
		t.Fatalf("expected a suspension at the last allowed turn, got %+v err=%v", result, err)
	}
	if result.Suspended.Iter != 2 {
		t.Fatalf("Iter = %d, want 2 (third turn)", result.Suspended.Iter)
	}
	if model.calls != 3 {
		t.Fatalf("setup: model.calls = %d, want 3", model.calls)
	}

	resumed, err := runner.runLoopResume(context.Background(), result.Suspended, "用 Redis")
	if err != nil {
		t.Fatalf("runLoopResume error: %v", err)
	}
	if resumed.FinishReason != HandoffReasonMaxIterations {
		t.Errorf("FinishReason = %q, want max_iterations (budget carried, not reset)", resumed.FinishReason)
	}
	if model.calls != 4 {
		t.Errorf("model.calls = %d, want 4 (exactly one more turn available)", model.calls)
	}
}

// TestAgentResumeNestedCascade: answering the top handle resumes the leaf, whose
// result is fed back into the top's pending handoff slot, which then resumes and
// delivers on the TOP handle's channel (the only channel the model can poll).
func TestAgentResumeNestedCascade(t *testing.T) {
	leafModel := submitAfterResumeModel("子代理结论")
	topModel := submitAfterResumeModel("顶层结论")
	runner := budgetRunner(topModel, readSpecExec())
	runner.SetSuspendedRegistrar(nil) // entries are fabricated here
	e := resumeEngine(t, runner)
	e.initBackgroundTasks()

	leaf := e.RegisterSuspended(leafSuspended(t, leafModel), AgentSub, "g")
	top := e.RegisterSuspended(&SuspendedRun{
		PendingToolID:      "h1",
		PendingToolMissing: true,
		ChildRunID:         leaf,
		Input:              Handoff{Agent: AgentSub, Goal: "g", StructuredResult: true, MaxIterations: 5},
		Model:              topModel,
		History: []ModelMessage{
			{Role: "system", Content: "sys"},
			{Role: "assistant", ToolCalls: []ModelToolCall{{ID: "h1", Type: "function", Function: ModelFunctionCall{Name: HandoffToolName}}}},
		},
	}, AgentSub, "g")

	msgs := e.processAgentResumeCalls(context.Background(), []ToolCallRequest{
		{ID: "call1", Name: AgentResumeToolName, Input: []byte(`{"run_id":"` + top + `","answer":"用 Redis"}`)},
	})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "resumed") {
		t.Fatalf("resume call must be acknowledged, got %+v", msgs)
	}

	// The cascade ends by delivering the top run's result on the top channel.
	e.bgMu.Lock()
	topTask := e.bgTasks[top]
	e.bgMu.Unlock()
	select {
	case got := <-topTask.result:
		if got == nil || got.FinishReason != HandoffReasonCompleted {
			t.Fatalf("top result = %+v, want a completed run", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cascade never delivered a result on the top handle")
	}
	// The parent's pending handoff slot was filled with the CHILD's digest, not
	// with the user's answer.
	if topModel.lastReq.Messages == nil {
		t.Fatal("the top run never ran")
	}
	found := false
	for _, m := range topModel.lastReq.Messages {
		if m.Role == "tool" && m.ToolCallID == "h1" && strings.Contains(m.Content, "子代理结论") {
			found = true
		}
	}
	if !found {
		t.Errorf("the top's handoff slot must carry the child digest (I8/I1); history=%+v", topModel.lastReq.Messages)
	}
}

// TestAgentPollReportsWaiting: polling a suspended entry reports the question
// and how to answer it (instead of "still running" forever).
func TestAgentPollReportsWaiting(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.initBackgroundTasks()
	id := e.RegisterSuspended(leafSuspended(t, submitAfterResumeModel("x")), AgentSub, "g")

	msgs := e.processAgentPollCalls([]ToolCallRequest{
		{ID: "p1", Name: AgentPollToolName, Input: []byte(`{"job_id":"` + id + `"}`)},
	})
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	c := msgs[0].Content
	if !strings.Contains(c, "waiting for user input") || !strings.Contains(c, "选哪个缓存？") || !strings.Contains(c, "agent_resume(") {
		t.Errorf("waiting poll must name the question and agent_resume, got %q", c)
	}
	e.bgMu.Lock()
	_, still := e.bgTasks[id]
	e.bgMu.Unlock()
	if !still {
		t.Error("polling a suspended job must not consume it")
	}
}

// TestAgentPollRejectsExpired: past the TTL a suspended entry is dropped and the
// poll explains it — fail-loud, no silent loss.
func TestAgentPollRejectsExpired(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.initBackgroundTasks()
	id := e.RegisterSuspended(leafSuspended(t, submitAfterResumeModel("x")), AgentSub, "g")
	e.bgMu.Lock()
	e.bgTasks[id].startAt = time.Now().Add(-2 * suspendedJobTTL)
	e.bgMu.Unlock()

	msgs := e.processAgentPollCalls([]ToolCallRequest{
		{ID: "p1", Name: AgentPollToolName, Input: []byte(`{"job_id":"` + id + `"}`)},
	})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "expired") {
		t.Fatalf("expired poll = %+v, want an expired notice", msgs)
	}
	e.bgMu.Lock()
	_, still := e.bgTasks[id]
	e.bgMu.Unlock()
	if still {
		t.Error("an expired entry must be dropped")
	}
}

// TestPinnedSummaryRendersWaitingState: the pinned job summary must not call a
// suspended job "running", and must point at agent_resume for a leaf.
func TestPinnedSummaryRendersWaitingState(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.initBackgroundTasks()
	leaf := e.RegisterSuspended(leafSuspended(t, submitAfterResumeModel("x")), AgentSub, "g")
	e.RegisterSuspended(&SuspendedRun{
		PendingToolID: "h1", PendingToolMissing: true, ChildRunID: leaf,
		Input: Handoff{Agent: AgentSub, Goal: "g"},
	}, AgentSub, "g")

	e.pendingPinnedMessages = nil
	e.injectBackgroundJobsSummary()
	if len(e.pendingPinnedMessages) != 1 {
		t.Fatalf("pinned messages = %d, want 1", len(e.pendingPinnedMessages))
	}
	p := e.pendingPinnedMessages[0]
	if !strings.Contains(p, "waiting for user input") || !strings.Contains(p, "agent_resume(") {
		t.Errorf("leaf entry must advertise the question/resume, got %q", p)
	}
	if !strings.Contains(p, "waiting for a sub-agent result") {
		t.Errorf("intermediate entry must say it waits for a child, got %q", p)
	}
	if strings.Contains(p, "— running") {
		t.Errorf("no suspended entry may render as running, got %q", p)
	}
}

// TestAgentResumeToolSpecRegistered: the tool must be offered to the model.
func TestAgentResumeToolSpecRegistered(t *testing.T) {
	for _, s := range (&Engine{tools: stubToolExecutor{}}).toolSpecsWithHandoff() {
		if s.Function.Name == AgentResumeToolName {
			return
		}
	}
	t.Fatal("toolSpecsWithHandoff must include agent_resume")
}

// TestDigestHandleOnlyAtTopLevel: only the top-level delegation renders its
// handle into the digest. A nested digest is inlined into its parent's Summary,
// so rendering its own handle there would hand the model a handle it must not
// use (the chain's middle is not answerable by the user).
func TestDigestHandleOnlyAtTopLevel(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "选哪个？", Questions: []string{"选哪个？"},
		FinishReason: HandoffReasonAwaitingUser,
		Suspended:    &SuspendedRun{Question: "选哪个？", Input: Handoff{Goal: "g"}},
	}})
	call := ToolCallRequest{ID: "h1", Name: HandoffToolName, Input: json.RawMessage(`{"agent":"sub","goal":"g"}`)}
	resolve := func(id AgentID) (Agent, error) { return reg.Get(id) }

	top := runHandoff(context.Background(), call, handoffOptions{
		resolve: resolve, depth: 0,
		register: func(*SuspendedRun, AgentID, string) string { return "bg-1" },
	})
	if !strings.Contains(top.Digest, "bg-1") {
		t.Errorf("top-level digest must carry its handle, got %q", top.Digest)
	}
	nested := runHandoff(context.Background(), call, handoffOptions{
		resolve: resolve, depth: 1,
		register: func(*SuspendedRun, AgentID, string) string { return "bg-2" },
	})
	if strings.Contains(nested.Digest, "bg-2") {
		t.Errorf("nested digest must NOT expose its handle, got %q", nested.Digest)
	}
	if nested.RunID != "bg-2" {
		t.Errorf("RunID = %q, want bg-2 (the handle still travels for the cascade)", nested.RunID)
	}
}

// TestFollowUpPinPointsAtResume: the pinned follow-up for awaiting_user must
// point at agent_resume; other reasons keep the re-delegate wording.
func TestFollowUpPinPointsAtResume(t *testing.T) {
	zh := buildHandoffFollowUp(HandoffReasonAwaitingUser, true)
	if !strings.Contains(zh, "agent_resume") {
		t.Errorf("awaiting_user pin must mention agent_resume, got %q", zh)
	}
	en := buildHandoffFollowUp(HandoffReasonAwaitingUser, false)
	if !strings.Contains(en, "agent_resume") {
		t.Errorf("english awaiting_user pin must mention agent_resume, got %q", en)
	}
	other := buildHandoffFollowUp(HandoffReasonMaxIterations, true)
	if strings.Contains(other, "agent_resume") {
		t.Errorf("other reasons keep the re-delegate wording, got %q", other)
	}
}
