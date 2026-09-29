package engine

import (
	"context"
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
