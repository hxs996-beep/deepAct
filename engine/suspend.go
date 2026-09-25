package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// suspendedJobTTL bounds how long a suspended run may wait for its answer. It is
// checked lazily (poll / resume / summary rendering) — there is no timer, so an
// entry nobody looks at never expires by itself.
const suspendedJobTTL = 30 * time.Minute

// AgentResumeParams is the argument schema of the agent_resume tool call.
type AgentResumeParams struct {
	RunID  string `json:"run_id"`
	Answer string `json:"answer"`
}

// suspendedJobExpiredLocked reports whether a suspended entry is past its TTL.
// Callers must hold bgMu (it only reads the entry).
func (e *Engine) suspendedJobExpiredLocked(t *bgTask) bool {
	return t != nil && t.state == bgStateAwaitingUser &&
		!t.startAt.IsZero() && time.Since(t.startAt) > suspendedJobTTL
}

// formatSuspendedPoll renders the agent_poll answer for a suspended entry: it
// names the question (leaf) or the child it waits for (intermediate), and how to
// answer it. Only the leaf is answerable by the user.
func formatSuspendedPoll(id string, t *bgTask) string {
	if t.suspended != nil && t.suspended.Question != "" {
		return fmt.Sprintf("Job %s (%s) is waiting for user input: %s\nAnswer it with agent_resume(%s, <answer>) — the run continues with its original context and you fetch the result with agent_poll(%s).",
			id, t.agent, t.suspended.Question, id, id)
	}
	return fmt.Sprintf("Job %s (%s) is waiting for a sub-agent result (it delegated further). Poll again later.", id, t.agent)
}

// processAgentResumeCalls handles agent_resume: it feeds the user's answer back
// into the suspended run and lets it continue in the background.
//
// The model names the handle it saw in the digest, which is always the TOP of a
// delegation chain. Because the top is waiting for its CHILD's result (not for
// the user), the answer must be delivered to the LEAF — the run that actually
// called ask_user. That is what findResumeLeafLocked walks to; writing the
// answer into the top would put it in a handoff tool slot.
func (e *Engine) processAgentResumeCalls(ctx context.Context, calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != AgentResumeToolName {
			continue
		}
		var params AgentResumeParams
		if err := json.Unmarshal(call.Input, &params); err != nil ||
			strings.TrimSpace(params.RunID) == "" || strings.TrimSpace(params.Answer) == "" {
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    "Error: agent_resume requires a non-empty run_id and answer",
				Timestamp:  time.Now(),
			})
			continue
		}

		leafID, leaf, err := e.takeLeafForResume(ctx, params.RunID)
		if err != nil {
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    err.Error(),
				Timestamp:  time.Now(),
			})
			continue
		}
		go e.resumeSuspendedJob(ctx, leafID, leaf, params.Answer)
		msgs = append(msgs, Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("Job %s resumed with the answer; it continues in the background. Fetch the result with agent_poll(%s).", leafID, params.RunID),
			Timestamp:  time.Now(),
		})
	}
	return msgs
}

// takeLeafForResume validates the named handle, drills down to the leaf of its
// chain and marks that leaf running (claiming it, so a concurrent resume cannot
// take it too). The returned SuspendedRun is the leaf's breakpoint.
func (e *Engine) takeLeafForResume(ctx context.Context, runID string) (string, *SuspendedRun, error) {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()

	task, ok := e.bgTasks[runID]
	if !ok {
		return "", nil, fmt.Errorf("agent_resume: job %q not found (it may have been consumed, expired or dropped). Re-delegate the task instead", runID)
	}
	if task.state != bgStateAwaitingUser || task.suspended == nil {
		return "", nil, fmt.Errorf("agent_resume: job %q is not waiting for user input (state=%q). Use agent_poll(%s) to see its status", runID, task.state, runID)
	}
	if e.suspendedJobExpiredLocked(task) {
		delete(e.bgTasks, runID)
		return "", nil, fmt.Errorf("agent_resume: job %q expired (suspended for over %s) and can no longer be resumed. Re-delegate the task instead", runID, suspendedJobTTL)
	}

	// Drill to the leaf: a chain entry waits for its child, so only the entry
	// without a child is the one the user must answer.
	leafID, leaf := runID, task.suspended
	for i := 0; i < 16; i++ {
		if leaf.ChildRunID == "" {
			break
		}
		next, ok := e.bgTasks[leaf.ChildRunID]
		if !ok || next.suspended == nil {
			return "", nil, fmt.Errorf("agent_resume: job %q is waiting for sub-agent %s, whose state is gone; re-delegate the task instead", runID, leaf.ChildRunID)
		}
		leafID, leaf = leaf.ChildRunID, next.suspended
	}
	if leaf.Question == "" {
		return "", nil, fmt.Errorf("agent_resume: job %q has no pending question to answer", runID)
	}

	leafTask := e.bgTasks[leafID]
	if leafTask == nil {
		return "", nil, fmt.Errorf("agent_resume: leaf job %q is gone; re-delegate the task instead", leafID)
	}
	leafTask.state = bgStateRunning
	leafTask.suspended = nil
	leafTask.startAt = time.Now()
	leafTask.ctx, leafTask.cancel = context.WithCancel(ctx)
	return leafID, leaf, nil
}

// resumeSuspendedJob continues a claimed leaf run in the background and reports
// its outcome. It runs on its own goroutine: the resumed run must not block the
// turn that answered the question.
func (e *Engine) resumeSuspendedJob(ctx context.Context, leafID string, s *SuspendedRun, answer string) {
	agent, err := e.agents.Get(s.Input.Agent)
	if err != nil {
		e.finishResumedJob(ctx, leafID, &HandoffResult{
			Summary:      fmt.Sprintf("(resume failed: agent %s not found)", s.Input.Agent),
			Blocked:      true,
			BlockedBy:    "resume_failed",
			FinishReason: HandoffReasonError,
		})
		return
	}
	resumer, ok := agent.(interface {
		RunSuspended(context.Context, *SuspendedRun, string) (*HandoffResult, error)
	})
	if !ok {
		e.finishResumedJob(ctx, leafID, &HandoffResult{
			Summary:      "(resume failed: this agent does not support resuming)",
			Blocked:      true,
			BlockedBy:    "resume_unsupported",
			FinishReason: HandoffReasonError,
		})
		return
	}
	result, runErr := resumer.RunSuspended(ctx, s, answer)
	if result == nil {
		summary := "(no result)"
		if runErr != nil {
			summary = "(sub-agent error: " + runErr.Error() + ")"
		}
		result = &HandoffResult{Summary: summary, Blocked: true, BlockedBy: "sub_agent_error", FinishReason: HandoffReasonError}
	}
	if result.Usage != nil {
		e.accumulateUsage(result.Usage)
	}
	e.finishResumedJob(ctx, leafID, result)
}

// finishResumedJob reports a resumed run's outcome: it either suspends again
// (the run asked another question — the chain stops here, waiting for a new
// answer) or cascades upward. The cascade fills the parent's pending handoff
// response with this child's digest and resumes the parent, recursively, until
// no parent is left — then the result is delivered on the top handle's channel
// for agent_poll.
// ctx is the context of the Run that answered the question: the whole resume
// chain (leaf → parents) runs on it, so Esc still cancels a resumed run and,
// like every async job, it never outlives that Run.
func (e *Engine) finishResumedJob(ctx context.Context, id string, result *HandoffResult) {
	e.bgMu.Lock()
	task := e.bgTasks[id]
	if task == nil {
		e.bgMu.Unlock()
		return // dropped meanwhile (TTL/history swap): nothing to report
	}
	if result.Suspended != nil {
		// Suspended again: keep this entry waiting, refresh its TTL.
		task.state = bgStateAwaitingUser
		task.suspended = result.Suspended
		task.childRunID = result.Suspended.ChildRunID
		task.startAt = time.Now()
		task.ctx, task.cancel = nil, nil
		e.bgMu.Unlock()
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{Type: "agent_done", Name: string(task.agent), Detail: result.Summary})
		}
		return
	}
	// Find the parent that waits for THIS entry.
	parentID := ""
	for pid, pt := range e.bgTasks {
		if pt.childRunID == id && pt.state == bgStateAwaitingUser {
			parentID = pid
			break
		}
	}
	e.bgMu.Unlock()

	if parentID == "" {
		// Top of the chain: deliver on its own channel for agent_poll.
		select {
		case task.result <- result:
		default:
		}
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{Type: "agent_done", Name: string(task.agent), Detail: result.Summary})
		}
		return
	}

	// Cascade: hand this child's digest to the parent and resume it.
	_, parentSuspended, err := e.takeParentForCascade(ctx, parentID)
	if err != nil {
		loopLog.Printf("resume cascade: %v (child %s result dropped)", err, id)
		return
	}
	go e.resumeSuspendedJob(ctx, parentID, parentSuspended, formatHandoffResult(result, e.isChinese))
}

// takeParentForCascade claims a parent entry for the cascade (mirrors
// takeLeafForResume but for the layer above).
func (e *Engine) takeParentForCascade(ctx context.Context, parentID string) (string, *SuspendedRun, error) {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	p, ok := e.bgTasks[parentID]
	if !ok || p.suspended == nil {
		return "", nil, fmt.Errorf("parent job %s is gone", parentID)
	}
	if p.state != bgStateAwaitingUser {
		return "", nil, fmt.Errorf("parent job %s is not waiting (state=%q)", parentID, p.state)
	}
	suspended := p.suspended
	p.state = bgStateRunning
	p.suspended = nil
	p.startAt = time.Now()
	p.ctx, p.cancel = context.WithCancel(ctx)
	return parentID, suspended, nil
}
