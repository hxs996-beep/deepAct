package engine

import (
	"context"
	"fmt"
	"testing"
)

// TestSuspendedEntrySurvivesRunEnd: I4 — the Run's exit path drops and cancels
// running jobs, but must KEEP awaiting_user entries: they hold no goroutine and
// no request, and they are a question the user still owes an answer to.
func TestSuspendedEntrySurvivesRunEnd(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.initBackgroundTasks()

	runningCtx, runningCancel := context.WithCancel(context.Background())
	suspendedCtx, suspendedCancel := context.WithCancel(context.Background())
	defer suspendedCancel()

	e.bgMu.Lock()
	e.bgTasks["bg-1"] = &bgTask{id: "bg-1", state: bgStateRunning, ctx: runningCtx, cancel: runningCancel}
	e.bgTasks["bg-2"] = &bgTask{id: "bg-2", state: bgStateAwaitingUser, ctx: suspendedCtx, cancel: suspendedCancel,
		suspended: &SuspendedRun{Input: Handoff{Goal: "g"}}}
	e.bgMu.Unlock()

	e.cancelBackgroundTasks()

	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	if _, ok := e.bgTasks["bg-2"]; !ok {
		t.Fatal("awaiting_user entry must survive Run end")
	}
	if _, ok := e.bgTasks["bg-1"]; ok {
		t.Error("running entry must be dropped at Run end")
	}
	select {
	case <-runningCtx.Done():
	default:
		t.Error("running entry must be cancelled")
	}
	select {
	case <-suspendedCtx.Done():
		t.Error("awaiting_user entry must NOT be cancelled — it has no in-flight work")
	default:
	}
}

// TestSuspendedCountSeparateFromInFlight: I5 — suspended entries hold no LLM
// slot, so they must not be counted as async outstanding work.
func TestSuspendedCountSeparateFromInFlight(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.initBackgroundTasks()
	e.bgMu.Lock()
	for i := 1; i <= 3; i++ {
		e.bgTasks[fmt.Sprintf("bg-%d", i)] = &bgTask{id: "x", state: bgStateAwaitingUser}
	}
	e.bgTasks["bg-9"] = &bgTask{id: "bg-9", state: bgStateRunning}
	e.bgMu.Unlock()

	if n := e.inFlightCount(); n != 1 {
		t.Errorf("inFlightCount() = %d, want 1 (suspended entries hold no LLM slot)", n)
	}
	if n := e.suspendedCount(); n != 3 {
		t.Errorf("suspendedCount() = %d, want 3", n)
	}
}

// TestAsyncCapIgnoresSuspendedEntries: I5 at the dispatch gate — a suspended
// entry must not make a new async dispatch look "at cap".
func TestAsyncCapIgnoresSuspendedEntries(t *testing.T) {
	a := &mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "完成", FinishReason: HandoffReasonCompleted,
	}}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()

	// Fill the table with suspended entries only; cap is the default 8, so a
	// dispatch that counted them would be refused.
	e.bgMu.Lock()
	for i := 1; i <= defaultMaxOutstandingAsyncSubAgents; i++ {
		e.bgTasks[fmt.Sprintf("bg-%d", i)] = &bgTask{id: "x", state: bgStateAwaitingUser}
	}
	e.bgMu.Unlock()

	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "任务", Async: true,
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("suspended entries must not consume async slots; got %+v", res)
	}
	e.cancelBackgroundTasks()
}
