package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// Async outstanding-cap tests: bgTasks counts dispatched-but-uncollected jobs
// (a slot frees only when agent_poll consumes a done result — turn.go deletes
// on poll), the cap fails loud (async semantics require an immediate return),
// and the check runs inside the bgMu critical section.

func newOutstandingTestEngine(cap int, agent Agent) *Engine {
	reg := NewAgentRegistry()
	reg.Register(agent)
	return &Engine{
		agents:    reg,
		isChinese: true,
		state:     &TaskState{},
		config:    EngineConfig{MaxOutstandingAsyncSubAgents: cap},
	}
}

func asyncDispatchParams(goal string) HandoffToAgentParams {
	return HandoffToAgentParams{Agent: "sub", Goal: goal, Async: true}
}

// waitJobPublished blocks until the background goroutine has published the
// job's result on its channel. Mirrors the wait in turn_agent_poll_test.go:
// probe with len(), never consume with <-task.result — consuming it would make
// processAgentPollCalls see an empty channel and report "still running".
func waitJobPublished(t *testing.T, e *Engine, jobID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.bgMu.Lock()
		task := e.bgTasks[jobID]
		e.bgMu.Unlock()
		if task != nil && len(task.result) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("background job %s never published its result", jobID)
}

// TestAsyncOutstanding_FailLoudAtCap: with cap=2, the third dispatch errors
// with outstanding wording and an agent_poll pointer — the model can collect
// existing results and retry. (Two distinct blocking agents: blockingAgent's
// start channel allows a single Run, so the second slot gets its own agent.)
func TestAsyncOutstanding_FailLoudAtCap(t *testing.T) {
	a1 := &blockingAgent{id: "one", start: make(chan struct{}), released: make(chan struct{})}
	a2 := &blockingAgent{id: "two", start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a1)
	reg.Register(a2)
	e := &Engine{
		agents:    reg,
		isChinese: true,
		state:     &TaskState{},
		config:    EngineConfig{MaxOutstandingAsyncSubAgents: 2},
	}

	for _, id := range []string{"one", "two"} {
		res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
			Agent: id, Goal: "任务", Async: true,
		}, 0, "中文")
		if err != nil || res.Status != "ok" {
			t.Fatalf("dispatch %s should succeed, got res=%+v err=%v", id, res, err)
		}
	}

	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "one", Goal: "任务", Async: true,
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("expected error status at cap, got %+v", res)
	}
	if !strings.Contains(res.Digest, "outstanding") || !strings.Contains(res.Digest, "agent_poll") {
		t.Errorf("digest must name outstanding and point at agent_poll, got %q", res.Digest)
	}
	// Cleanup: release the blocked goroutines and drain the table.
	close(a1.released)
	close(a2.released)
	e.cancelBackgroundTasks()
}

// TestAsyncOutstanding_PollFreesSlot: a poll that consumes a done result
// removes the job from the table, freeing a slot for a new dispatch.
func TestAsyncOutstanding_PollFreesSlot(t *testing.T) {
	a := &mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "完成", FinishReason: HandoffReasonCompleted,
	}}
	e := newOutstandingTestEngine(2, a)
	e.initBackgroundTasks()

	// Fill both slots with instantly-completing jobs (done but unpolled —
	// they still hold their slot).
	for i := 1; i <= 2; i++ {
		res, err := e.RunSubAgent(context.Background(), asyncDispatchParams("任务"), 0, "中文")
		if err != nil || res.Status != "ok" {
			t.Fatalf("dispatch %d should succeed, got res=%+v err=%v", i, res, err)
		}
	}
	res, err := e.RunSubAgent(context.Background(), asyncDispatchParams("任务"), 0, "中文")
	if err != nil || res.Status != "error" {
		t.Fatalf("third dispatch must fail at cap (done-but-unpolled still occupies), got res=%+v err=%v", res, err)
	}

	// The run executes on a background goroutine: wait until bg-1's result is
	// published, otherwise the poll races the goroutine and reports "still
	// running" (the state this test is meant to distinguish from "done").
	waitJobPublished(t, e, "bg-1")

	// Poll bg-1: the completed result is consumed, the entry is deleted.
	msgs := e.processAgentPollCalls([]ToolCallRequest{{
		ID: "p1", Name: AgentPollToolName, Input: []byte(`{"job_id":"bg-1"}`),
	}})
	if len(msgs) != 1 || strings.Contains(msgs[0].Content, "still running") {
		t.Fatalf("expected the done result delivered, got %+v", msgs)
	}

	// The freed slot admits a new dispatch.
	res, err = e.RunSubAgent(context.Background(), asyncDispatchParams("新任务"), 0, "中文")
	if err != nil || res.Status != "ok" || res.FinishReason != HandoffReasonAsyncRunning {
		t.Fatalf("dispatch after poll must succeed, got res=%+v err=%v", res, err)
	}
	if !strings.Contains(res.Digest, "bg-3") {
		t.Errorf("expected job_id bg-3, got %q", res.Digest)
	}
	e.cancelBackgroundTasks()
}

// TestAsyncOutstanding_DefaultCapWhenUnset: a bare Engine (config zero value)
// resolves the engine-side default — dispatches below the default all pass.
func TestAsyncOutstanding_DefaultCapWhenUnset(t *testing.T) {
	a := &mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "完成", FinishReason: HandoffReasonCompleted,
	}}
	e := &Engine{agents: func() *AgentRegistry {
		reg := NewAgentRegistry()
		reg.Register(a)
		return reg
	}(), isChinese: true, state: &TaskState{}}
	if got := e.maxOutstandingAsyncSubAgents(); got != defaultMaxOutstandingAsyncSubAgents {
		t.Fatalf("bare engine must resolve the default cap, got %d", got)
	}
	e.initBackgroundTasks()
	for i := 1; i <= defaultMaxOutstandingAsyncSubAgents; i++ {
		res, err := e.RunSubAgent(context.Background(), asyncDispatchParams("任务"), 0, "中文")
		if err != nil || res.Status != "ok" {
			t.Fatalf("dispatch %d within the default cap must succeed, got res=%+v err=%v", i, res, err)
		}
	}
	res, err := e.RunSubAgent(context.Background(), asyncDispatchParams("任务"), 0, "中文")
	if err != nil || res.Status != "error" {
		t.Fatalf("dispatch past the default cap must fail loud, got res=%+v err=%v", res, err)
	}
	e.cancelBackgroundTasks()
}

// TestAsyncOutstanding_ConcurrentDispatch: concurrent dispatches against one
// engine are race-free (bgMu-guarded check + table); under the cap all
// succeed, the check never over-admits. Run under -race.
func TestAsyncOutstanding_ConcurrentDispatch(t *testing.T) {
	a := &mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "完成", FinishReason: HandoffReasonCompleted,
	}}
	e := newOutstandingTestEngine(6, a)
	e.initBackgroundTasks()

	const n = 6
	var wg sync.WaitGroup
	statuses := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := e.RunSubAgent(context.Background(), asyncDispatchParams("并发任务"), 0, "中文")
			switch {
			case err != nil:
				statuses[i] = "err"
			case res.Status == "ok":
				statuses[i] = "ok"
			case res.Status == "error":
				statuses[i] = "error"
			}
		}(i)
	}
	wg.Wait()
	okCount := 0
	for _, s := range statuses {
		if s == "ok" {
			okCount++
		}
		if s == "err" {
			t.Fatal("dispatch returned an engine error")
		}
	}
	if okCount != 6 {
		t.Errorf("all 6 dispatches within cap must succeed, got %d ok", okCount)
	}

	// Give the instant agents a moment to settle, then clean up.
	time.Sleep(10 * time.Millisecond)
	e.cancelBackgroundTasks()
}
