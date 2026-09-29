package engine

import (
	"context"
	"strings"
	"testing"
	"time"
)

// firstJobID returns the single registered job's id. Tests dispatch exactly one
// job before calling it.
func firstJobID(t *testing.T, e *Engine) string {
	t.Helper()
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	for id := range e.bgTasks {
		return id
	}
	t.Fatal("no background job registered")
	return ""
}

// TestFinishBackgroundJob_ClosesDoneAndSetsBrief pins the two signals list/wait
// depend on: the done channel (completion, observable without consuming the
// result) and resultBrief (a one-line digest for display).
func TestFinishBackgroundJob_ClosesDoneAndSetsBrief(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "结果A", FinishReason: HandoffReasonCompleted,
	}})
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()

	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "g", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	jobID := firstJobID(t, e)
	waitJobPublished(t, e, jobID)

	e.bgMu.Lock()
	task := e.bgTasks[jobID]
	settled := agentJobSettled(task)
	brief := ""
	state := ""
	if task != nil {
		brief = task.resultBrief
		state = task.state
	}
	e.bgMu.Unlock()

	if !settled {
		t.Fatal("done channel must be closed once the result is published")
	}
	// done closing must imply the result is already readable (publish order:
	// result → brief → close(done), all under one lock).
	if got := len(task.result); got != 1 {
		t.Errorf("result depth = %d after done closed, want 1 (a waiter must find it)", got)
	}
	if brief == "" {
		t.Error("resultBrief must be set so list/wait can show a short result")
	}
	// Hard constraint: finishing must NOT touch state, or the async outstanding
	// cap would free the job's slot on completion instead of on collection.
	if state != bgStateRunning {
		t.Errorf("state = %q after finish, want %q (outstanding cap depends on it)", state, bgStateRunning)
	}
}

// TestFinishBackgroundJob_DoubleFinishIsIdempotent pins the closed-check: a
// cancellation racing a normal return must not panic or double-deliver.
func TestFinishBackgroundJob_DoubleFinishIsIdempotent(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.initBackgroundTasks()
	task := &bgTask{
		id: "bg-1", agent: AgentSub, goal: "g", state: bgStateRunning,
		startAt: time.Now(), result: make(chan *HandoffResult, 1), done: make(chan struct{}),
	}
	e.bgMu.Lock()
	e.bgTasks["bg-1"] = task
	e.bgMu.Unlock()

	e.finishBackgroundJob(task, &HandoffResult{Summary: "第一次"})
	e.finishBackgroundJob(task, &HandoffResult{Summary: "第二次"}) // must be a no-op

	if !channelClosed(task.done) {
		t.Fatal("done must be closed")
	}
	if got := len(task.result); got != 1 {
		t.Fatalf("result depth = %d, want 1 (a second finish must not deliver again)", got)
	}
}

// --- list_agents -------------------------------------------------------------

func TestListAgentsToolSpec_Registered(t *testing.T) {
	specs := (&Engine{tools: stubToolExecutor{}}).toolSpecsWithHandoff()
	for _, s := range specs {
		if s.Function.Name == ListAgentsToolName {
			return
		}
	}
	t.Fatal("toolSpecsWithHandoff must include list_agents")
}

func TestProcessListAgentsCalls_ShowsJobs(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	e.bgMu.Lock()
	e.bgTasks["bg-1"] = &bgTask{
		id: "bg-1", agent: "researcher", goal: "调研缓存",
		state: bgStateRunning, startAt: time.Now(), done: make(chan struct{}),
	}
	e.bgTasks["bg-2"] = &bgTask{
		id: "bg-2", agent: "critic", goal: "审查方案",
		state: bgStateAwaitingUser, startAt: time.Now(), done: make(chan struct{}),
	}
	e.bgMu.Unlock()

	msgs := e.processListAgentsCalls([]ToolCallRequest{{ID: "tc1", Name: ListAgentsToolName}})
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	lines := strings.Split(strings.TrimSpace(msgs[0].Content), "\n")
	if len(lines) != 2 {
		t.Fatalf("list has %d lines, want 2:\n%s", len(lines), msgs[0].Content)
	}
	// Ordering is part of the contract (id-sorted, stable output), and each
	// job's own state must be right — a substring check could not tell a
	// swapped running/awaiting_user mapping from a correct one.
	if !strings.HasPrefix(lines[0], "bg-1 | researcher | running") {
		t.Errorf("line 0 = %q, want the running researcher job first", lines[0])
	}
	if !strings.HasPrefix(lines[1], "bg-2 | critic | awaiting_user") {
		t.Errorf("line 1 = %q, want the awaiting_user critic job second", lines[1])
	}
	if !strings.Contains(lines[0], "调研缓存") || !strings.Contains(lines[1], "审查方案") {
		t.Errorf("each line must carry its own goal:\n%s", msgs[0].Content)
	}
}

func TestProcessListAgentsCalls_Empty(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	msgs := e.processListAgentsCalls([]ToolCallRequest{{ID: "tc1", Name: ListAgentsToolName}})
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Content != "No background sub-agent jobs." {
		t.Fatalf("empty list = %q, want the exact no-jobs sentence", msgs[0].Content)
	}
}

// TestBackgroundJobsPinnedInjection_ReportsDone pins the consistency fix: a
// finished-but-uncollected job (state still bgStateRunning, done closed) must
// be reported as done in the pinned summary, exactly as list_agents does.
func TestBackgroundJobsPinnedInjection_ReportsDone(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	done := &bgTask{
		id: "bg-1", agent: "researcher", goal: "调研", state: bgStateRunning,
		startAt: time.Now(), done: make(chan struct{}),
	}
	close(done.done)
	e.bgMu.Lock()
	e.bgTasks["bg-1"] = done
	e.bgMu.Unlock()

	e.pendingPinnedMessages = nil
	e.injectBackgroundJobsSummary()
	if len(e.pendingPinnedMessages) != 1 || !strings.Contains(e.pendingPinnedMessages[0], "done") {
		t.Fatalf("pinned summary = %v, want a done report", e.pendingPinnedMessages)
	}
	if !strings.Contains(e.pendingPinnedMessages[0], "bg-1") {
		t.Errorf("pinned summary = %q, want the job handle", e.pendingPinnedMessages[0])
	}
}

// TestRenderAgentList_DoneJobLine covers the two branches the other list tests
// miss — the done marker and the result brief — and pins the one-line contract
// for a model-supplied goal that contains newlines.
func TestRenderAgentList_DoneJobLine(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	done := &bgTask{
		id: "bg-3", agent: "sub", goal: "总结\n要点", state: bgStateRunning,
		startAt: time.Now(), done: make(chan struct{}), resultBrief: "结论A",
	}
	close(done.done)
	e.bgMu.Lock()
	e.bgTasks["bg-3"] = done
	e.bgMu.Unlock()

	out := e.renderAgentList()
	lines := strings.Split(out, "\n")
	if len(lines) != 1 {
		t.Fatalf("renderAgentList = %q, want exactly one line (goal newline must be collapsed)", out)
	}
	if !strings.HasPrefix(lines[0], "bg-3 | sub | done") {
		t.Errorf("done line = %q, want prefix %q", lines[0], "bg-3 | sub | done")
	}
	for _, want := range []string{"collect with agent_poll", "result: 结论A", "总结 要点"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("done line %q missing %q", lines[0], want)
		}
	}
}

// --- wait_agent --------------------------------------------------------------

func TestWaitAgent_ReturnsImmediatelyWhenDone(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "结果A", FinishReason: HandoffReasonCompleted,
	}})
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "g", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	jobID := firstJobID(t, e)
	waitJobPublished(t, e, jobID)

	start := time.Now()
	msgs := e.processWaitAgentCalls(context.Background(), []ToolCallRequest{{
		ID: "tc1", Name: WaitAgentToolName,
		Input: mustJSON(map[string]any{"job_ids": []string{jobID}, "timeout_ms": 5000}),
	}})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("wait on a finished job took %v, want immediate", elapsed)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "activity") {
		t.Fatalf("wait output = %+v, want an activity report", msgs)
	}
	// Assert the job's OWN line, not the fixed closing sentence (which
	// contains the word "done" unconditionally).
	doneLine := false
	for _, line := range strings.Split(msgs[0].Content, "\n") {
		if strings.HasPrefix(line, jobID+" | sub | done") {
			doneLine = true
		}
	}
	if !doneLine {
		t.Errorf("wait output = %q, want a %q line", msgs[0].Content, jobID+" | sub | done")
	}
	// wait_agent OBSERVES: it must not consume the result, or a later
	// agent_poll would report the job as gone.
	e.bgMu.Lock()
	task, still := e.bgTasks[jobID]
	depth := 0
	if task != nil {
		depth = len(task.result)
	}
	e.bgMu.Unlock()
	if !still {
		t.Fatal("wait_agent must not remove the job")
	}
	if depth != 1 {
		t.Errorf("result depth = %d after wait, want 1 (wait must not consume it)", depth)
	}
}

func TestWaitAgent_TimesOutOnRunningJob(t *testing.T) {
	a := &blockingAgent{id: "one", start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "one", Goal: "慢任务", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-a.start
	defer close(a.released)

	start := time.Now()
	msgs := e.processWaitAgentCalls(context.Background(), []ToolCallRequest{{
		ID: "tc1", Name: WaitAgentToolName,
		Input: mustJSON(map[string]any{"timeout_ms": 200}),
	}})
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("wait returned after %v, want it to block until the timeout", elapsed)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "timeout") {
		t.Fatalf("wait output = %+v, want a timeout report", msgs)
	}
}

func TestWaitAgent_WakesOnCompletion(t *testing.T) {
	a := &blockingAgent{id: "one", start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "one", Goal: "慢任务", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-a.start
	go func() {
		time.Sleep(300 * time.Millisecond)
		close(a.released)
	}()

	start := time.Now()
	msgs := e.processWaitAgentCalls(context.Background(), []ToolCallRequest{{
		ID: "tc1", Name: WaitAgentToolName,
		Input: mustJSON(map[string]any{"timeout_ms": 5000}),
	}})
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Errorf("wait returned after %v, want it to block until the job finished", elapsed)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "activity") {
		t.Fatalf("wait output = %+v, want an activity wake", msgs)
	}
}

func TestWaitAgent_CancelledContext(t *testing.T) {
	a := &blockingAgent{id: "one", start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "one", Goal: "慢任务", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-a.start
	defer close(a.released)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	msgs := e.processWaitAgentCalls(ctx, []ToolCallRequest{{
		ID: "tc1", Name: WaitAgentToolName,
		Input: mustJSON(map[string]any{"timeout_ms": 10000}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "cancelled") {
		t.Fatalf("wait output = %+v, want a cancelled report", msgs)
	}
}

func TestWaitAgent_EmptyTable(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	msgs := e.processWaitAgentCalls(context.Background(), []ToolCallRequest{{
		ID: "tc1", Name: WaitAgentToolName, Input: mustJSON(map[string]any{}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "No matching background jobs") {
		t.Fatalf("wait output = %+v, want the no-jobs sentence", msgs)
	}
}
