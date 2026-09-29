package engine

import (
	"context"
	"testing"
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
	if task != nil {
		brief = task.resultBrief
	}
	e.bgMu.Unlock()

	if !settled {
		t.Fatal("done channel must be closed once the result is published")
	}
	if brief == "" {
		t.Error("resultBrief must be set so list/wait can show a short result")
	}
}
