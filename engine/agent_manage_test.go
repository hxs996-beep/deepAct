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

// TestWaitAgent_SkipsRemovedJob: a job removed while the wait is blocking (here
// polled away from another goroutine) must not be reported — its handle is dead.
func TestWaitAgent_SkipsRemovedJob(t *testing.T) {
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
	jobID := firstJobID(t, e)

	go func() {
		time.Sleep(50 * time.Millisecond)
		e.bgMu.Lock()
		delete(e.bgTasks, jobID)
		e.bgMu.Unlock()
	}()

	msgs := e.processWaitAgentCalls(context.Background(), []ToolCallRequest{{
		ID: "tc1", Name: WaitAgentToolName,
		Input: mustJSON(map[string]any{"job_ids": []string{jobID}, "timeout_ms": 600}),
	}})
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	for _, line := range strings.Split(msgs[0].Content, "\n") {
		if strings.Contains(line, jobID+" |") {
			t.Errorf("wait reported a job that was removed while waiting: %q", line)
		}
	}
}

// --- send_message ------------------------------------------------------------

func TestDrainInbox_AppendsUserMessages(t *testing.T) {
	inbox := make(chan string, 3)
	inbox <- "先看 P0 那几项"
	inbox <- "  "
	inbox <- "顺便看下测试"

	history := drainInbox(inbox, nil)
	if len(history) != 2 {
		t.Fatalf("drained %d messages, want 2 (blank skipped)", len(history))
	}
	for _, m := range history {
		if m.Role != "user" {
			t.Errorf("drained message role = %q, want user", m.Role)
		}
	}
	if got := drainInbox(inbox, nil); len(got) != 0 {
		t.Error("a drained inbox must be empty")
	}
	if got := drainInbox(nil, nil); len(got) != 0 {
		t.Error("a nil inbox (sync/nested run) must be a no-op")
	}
}

func TestDrainInbox_AppendsToExistingHistory(t *testing.T) {
	inbox := make(chan string, 1)
	inbox <- "补充一点"
	history := []ModelMessage{{Role: "system", Content: "sys"}, {Role: "user", Content: "goal"}}
	got := drainInbox(inbox, history)
	if len(got) != 3 || got[2].Content != "补充一点" {
		t.Fatalf("history = %+v, want the message appended at the end", got)
	}
}

func TestSendMessageToolSpec_Registered(t *testing.T) {
	specs := (&Engine{tools: stubToolExecutor{}}).toolSpecsWithHandoff()
	for _, s := range specs {
		if s.Function.Name == SendMessageToolName {
			return
		}
	}
	t.Fatal("toolSpecsWithHandoff must include send_message")
}

func TestProcessSendMessageCalls_DeliversToRunningJob(t *testing.T) {
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
	jobID := firstJobID(t, e)

	msgs := e.processSendMessageCalls([]ToolCallRequest{{
		ID: "tc1", Name: SendMessageToolName,
		Input: mustJSON(map[string]string{"job_id": jobID, "message": "换个方向"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "delivered") {
		t.Fatalf("send_message output = %+v, want delivered", msgs)
	}
	e.bgMu.Lock()
	task := e.bgTasks[jobID]
	e.bgMu.Unlock()
	if task == nil || len(task.inbox) != 1 {
		t.Fatalf("inbox depth = %d, want 1", len(task.inbox))
	}
	if got := <-task.inbox; got != "换个方向" {
		t.Errorf("inbox message = %q, want the sent text", got)
	}
}

func TestProcessSendMessageCalls_UnknownJob(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	msgs := e.processSendMessageCalls([]ToolCallRequest{{
		ID: "tc1", Name: SendMessageToolName,
		Input: mustJSON(map[string]string{"job_id": "bg-999", "message": "hi"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "not found") {
		t.Fatalf("send_message output = %+v, want not found", msgs)
	}
}

func TestProcessSendMessageCalls_RejectsSuspendedJob(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	e.bgMu.Lock()
	e.bgTasks["bg-1"] = &bgTask{
		id: "bg-1", agent: "sub", goal: "g", state: bgStateAwaitingUser,
		startAt: time.Now(), done: make(chan struct{}), inbox: make(chan string, inboxBufferSize),
	}
	e.bgMu.Unlock()

	msgs := e.processSendMessageCalls([]ToolCallRequest{{
		ID: "tc1", Name: SendMessageToolName,
		Input: mustJSON(map[string]string{"job_id": "bg-1", "message": "hi"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "not running") {
		t.Fatalf("send_message output = %+v, want a not-running refusal", msgs)
	}
	if !strings.Contains(msgs[0].Content, AgentResumeToolName) {
		t.Errorf("refusal must point at %s, got %q", AgentResumeToolName, msgs[0].Content)
	}
}

func TestProcessSendMessageCalls_RejectsFullInbox(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	full := make(chan string, 1)
	full <- "占位"
	e.bgMu.Lock()
	e.bgTasks["bg-1"] = &bgTask{
		id: "bg-1", agent: "sub", goal: "g", state: bgStateRunning,
		startAt: time.Now(), done: make(chan struct{}), inbox: full,
	}
	e.bgMu.Unlock()

	msgs := e.processSendMessageCalls([]ToolCallRequest{{
		ID: "tc1", Name: SendMessageToolName,
		Input: mustJSON(map[string]string{"job_id": "bg-1", "message": "hi"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "inbox is full") {
		t.Fatalf("send_message output = %+v, want an inbox-full refusal", msgs)
	}
}

func TestProcessSendMessageCalls_RequiresBothFields(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	msgs := e.processSendMessageCalls([]ToolCallRequest{{
		ID: "tc1", Name: SendMessageToolName,
		Input: mustJSON(map[string]string{"job_id": "bg-1"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "non-empty job_id and message") {
		t.Fatalf("send_message output = %+v, want a validation error", msgs)
	}
}

// inboxCaptureAgent records the Handoff it was given (and blocks until
// released), so a test can prove the engine actually handed the run an inbox —
// without that wiring drainInbox would never see anything.
type inboxCaptureAgent struct {
	id      AgentID
	got     chan Handoff
	release chan struct{}
}

func (a *inboxCaptureAgent) ID() AgentID     { return a.id }
func (a *inboxCaptureAgent) Spec() AgentSpec { return AgentSpec{ID: a.id} }
func (a *inboxCaptureAgent) Run(_ context.Context, in Handoff) (*HandoffResult, error) {
	a.got <- in
	<-a.release
	return &HandoffResult{Summary: "ok", FinishReason: HandoffReasonCompleted}, nil
}

// TestSendMessage_ReachesTheSubAgentRun proves the end-to-end wiring: an async
// dispatch hands the run the very channel send_message writes to, so a message
// sent through the tool is what drainInbox would read inside the sub-agent loop.
func TestSendMessage_ReachesTheSubAgentRun(t *testing.T) {
	a := &inboxCaptureAgent{id: "cap", got: make(chan Handoff, 1), release: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "cap", Goal: "g", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	runInput := <-a.got
	if runInput.Inbox == nil {
		t.Fatal("dispatchAsync must hand the run an inbox")
	}

	msgs := e.processSendMessageCalls([]ToolCallRequest{{
		ID: "tc1", Name: SendMessageToolName,
		Input: mustJSON(map[string]string{"job_id": firstJobID(t, e), "message": "改方向"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "delivered") {
		t.Fatalf("send_message output = %+v, want delivered", msgs)
	}
	select {
	case got := <-runInput.Inbox:
		if got != "改方向" {
			t.Errorf("run inbox received %q, want the sent text", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the sent message never reached the run's inbox")
	}
	close(a.release)
}

// TestRunLoopFrom_DrainsInboxIntoTheRequest pins the drainInbox CALL SITE: the
// sub-agent loop reads the inbox at the top of an iteration, so the message
// reaches the model. Without that call every unit test above still passes.
func TestRunLoopFrom_DrainsInboxIntoTheRequest(t *testing.T) {
	model := &stubCompleteModel{resp: "done"}
	r := NewSubAgentRunner(model, stubToolExecutor{}, nil, "deepact-chat")
	r.workDir = t.TempDir()

	inbox := make(chan string, 1)
	inbox <- "运行中补充的信息"

	if _, err := r.runLoop(context.Background(), Handoff{
		Agent: AgentSub, Goal: "g", NoNudge: true, Inbox: inbox,
	}, "", 3); err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	found := false
	for _, m := range model.last.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "运行中补充的信息") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the inbox message never reached the model request: %+v", model.last.Messages)
	}
	if got := len(inbox); got != 0 {
		t.Errorf("inbox depth = %d after the run, want 0 (it must be drained)", got)
	}
}

// --- interrupt_agent / close_agent -------------------------------------------

// ctxWaitAgent blocks until its context is cancelled, then reports a cancelled
// run — the shape a real sub-agent takes when interrupt_agent fires.
type ctxWaitAgent struct {
	id     AgentID
	start  chan struct{}
	cancel chan struct{}
}

func (a *ctxWaitAgent) ID() AgentID     { return a.id }
func (a *ctxWaitAgent) Spec() AgentSpec { return AgentSpec{ID: a.id} }
func (a *ctxWaitAgent) Run(ctx context.Context, _ Handoff) (*HandoffResult, error) {
	close(a.start)
	<-ctx.Done()
	close(a.cancel)
	return &HandoffResult{
		Summary: "(cancelled)", Blocked: true, BlockedBy: "cancelled",
		FinishReason: HandoffReasonCancelled,
	}, nil
}

func TestLifecycleTools_Registered(t *testing.T) {
	specs := (&Engine{tools: stubToolExecutor{}}).toolSpecsWithHandoff()
	want := map[string]bool{InterruptAgentToolName: false, CloseAgentToolName: false}
	for _, s := range specs {
		if _, ok := want[s.Function.Name]; ok {
			want[s.Function.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("toolSpecsWithHandoff must include %s", name)
		}
	}
}

func TestInterruptAgent_StopsRunningJob(t *testing.T) {
	a := &ctxWaitAgent{id: "one", start: make(chan struct{}), cancel: make(chan struct{})}
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
	jobID := firstJobID(t, e)

	msgs := e.processInterruptAgentCalls([]ToolCallRequest{{
		ID: "tc1", Name: InterruptAgentToolName,
		Input: mustJSON(map[string]string{"job_id": jobID}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "interrupted") {
		t.Fatalf("interrupt output = %+v, want interrupted", msgs)
	}
	select {
	case <-a.cancel:
	case <-time.After(2 * time.Second):
		t.Fatal("interrupt must cancel the running sub-agent's context")
	}
	waitJobPublished(t, e, jobID)

	// The job STAYS collectable: interrupt keeps the partial result.
	poll := e.processAgentPollCalls([]ToolCallRequest{{
		ID: "tc2", Name: AgentPollToolName, Input: mustJSON(map[string]string{"job_id": jobID}),
	}})
	if len(poll) != 1 || !strings.Contains(poll[0].Content, "done") {
		t.Fatalf("poll after interrupt = %+v, want done", poll)
	}
}

func TestInterruptAgent_UnknownJob(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	msgs := e.processInterruptAgentCalls([]ToolCallRequest{{
		ID: "tc1", Name: InterruptAgentToolName, Input: mustJSON(map[string]string{"job_id": "bg-999"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "not found") {
		t.Fatalf("interrupt output = %+v, want not found", msgs)
	}
}

func TestInterruptAgent_SuspendedJobIsNotRunning(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	e.bgMu.Lock()
	e.bgTasks["bg-1"] = &bgTask{
		id: "bg-1", agent: "sub", goal: "g", state: bgStateAwaitingUser,
		startAt: time.Now(), done: make(chan struct{}),
	}
	e.bgMu.Unlock()

	msgs := e.processInterruptAgentCalls([]ToolCallRequest{{
		ID: "tc1", Name: InterruptAgentToolName, Input: mustJSON(map[string]string{"job_id": "bg-1"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "not running") {
		t.Fatalf("interrupt output = %+v, want a not-running refusal", msgs)
	}
}

// TestCloseAgent_RemovesJobAndDescendants pins the subtree walk: childRunID
// points from a waiting parent DOWN to its pending child, so closing a job must
// follow that chain and leave unrelated jobs alone.
func TestCloseAgent_RemovesJobAndDescendants(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	e.bgMu.Lock()
	e.bgTasks["bg-1"] = &bgTask{
		id: "bg-1", agent: "one", goal: "父", state: bgStateAwaitingUser,
		startAt: time.Now(), done: make(chan struct{}), childRunID: "bg-2",
	}
	e.bgTasks["bg-2"] = &bgTask{
		id: "bg-2", agent: "one", goal: "子", state: bgStateRunning,
		startAt: time.Now(), done: make(chan struct{}), cancel: func() {},
	}
	e.bgTasks["bg-3"] = &bgTask{
		id: "bg-3", agent: "one", goal: "无关", state: bgStateRunning,
		startAt: time.Now(), done: make(chan struct{}),
	}
	e.bgMu.Unlock()

	msgs := e.processCloseAgentCalls([]ToolCallRequest{{
		ID: "tc1", Name: CloseAgentToolName, Input: mustJSON(map[string]string{"job_id": "bg-1"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "Closed 2 job") {
		t.Fatalf("close output = %+v, want 2 jobs closed", msgs)
	}
	e.bgMu.Lock()
	_, has1 := e.bgTasks["bg-1"]
	_, has2 := e.bgTasks["bg-2"]
	_, has3 := e.bgTasks["bg-3"]
	e.bgMu.Unlock()
	if has1 || has2 {
		t.Errorf("close_agent must remove the job and its descendants (bg-1=%v bg-2=%v)", has1, has2)
	}
	if !has3 {
		t.Error("close_agent must NOT touch an unrelated job")
	}
}

func TestCloseAgent_DropsSuspendedWithWarning(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	e.bgMu.Lock()
	e.bgTasks["bg-1"] = &bgTask{
		id: "bg-1", agent: "sub", goal: "g", state: bgStateAwaitingUser,
		startAt: time.Now(), done: make(chan struct{}), suspended: &SuspendedRun{Question: "q"},
	}
	e.bgMu.Unlock()

	msgs := e.processCloseAgentCalls([]ToolCallRequest{{
		ID: "tc1", Name: CloseAgentToolName, Input: mustJSON(map[string]string{"job_id": "bg-1"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "suspended") {
		t.Fatalf("close output = %+v, want a suspended-dropped warning", msgs)
	}
}

func TestCloseAgent_UnknownJob(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	msgs := e.processCloseAgentCalls([]ToolCallRequest{{
		ID: "tc1", Name: CloseAgentToolName, Input: mustJSON(map[string]string{"job_id": "bg-999"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "not found") {
		t.Fatalf("close output = %+v, want not found", msgs)
	}
}
