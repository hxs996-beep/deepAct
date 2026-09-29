package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// This file implements the Run-scoped sub-agent management surface: the main
// agent's ability to observe, wait for, message, interrupt and close the
// background jobs it dispatched. All of them are ENGINE built-ins intercepted
// in executeTurn (like agent_poll / agent_resume): they never enter the tool
// executor, and sub-agents never see them (their tool set is filtered in
// sub_agent.go). Every intercepted call MUST get exactly one tool response, or
// the next request breaks the assistant(tool_calls) → tool contract.

// --- Job view primitives -----------------------------------------------------
// A bgTask's completion is a SIGNAL (the done channel), not the state field:
// a finished-but-uncollected job keeps state == bgStateRunning so the async
// outstanding cap still holds its slot until agent_poll consumes the result.

// channelClosed reports whether ch has been closed, without consuming a value.
// A nil channel is never closed.
func channelClosed(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// agentJobSettled reports whether waiting on a job is pointless: it published a
// result, or it is suspended waiting for the user.
//
// Callers MUST hold bgMu: t.state is rewritten by background goroutines under
// that lock (suspendExistingJob / takeLeafForResume / takeParentForCascade /
// finishResumedJob).
func agentJobSettled(t *bgTask) bool {
	if t == nil {
		return true
	}
	return t.state == bgStateAwaitingUser || channelClosed(t.done)
}

// agentJobState returns the job's state as a bare token — "running", "done" or
// "awaiting_user" — for renderers to decorate. Keeping the token free of
// instructions means callers can compare it instead of parsing prose.
//
// Callers MUST hold bgMu (it reads t.state).
func agentJobState(t *bgTask) string {
	if t == nil {
		return "unknown"
	}
	switch {
	case t.state == bgStateAwaitingUser:
		return "awaiting_user"
	case channelClosed(t.done):
		return "done"
	default:
		return "running"
	}
}

// agentJobLine renders one job as a single line. Callers must hold bgMu.
func agentJobLine(t *bgTask, now time.Time) string {
	if t == nil {
		return "unknown"
	}
	state := agentJobState(t)
	elapsed := now.Sub(t.startAt).Round(time.Second)
	// goal is model-supplied: collapse newlines so the one-job-per-line
	// contract holds (a raw \n would split one job into two lines).
	goal := strings.ReplaceAll(strings.TrimSpace(t.goal), "\n", " ")
	line := fmt.Sprintf("%s | %s | %s | %s | %s", t.id, t.agent, state, elapsed, goal)
	if t.resultBrief != "" {
		line += " | result: " + t.resultBrief
	}
	if state == "done" {
		line += " | collect with agent_poll"
	}
	return line
}

// agentToolMessage builds the tool-role Message answering one intercepted call.
func agentToolMessage(callID, content string) Message {
	return Message{Role: "tool", ToolCallID: callID, Content: content, Timestamp: time.Now()}
}

// renderAgentList renders every background job, in id order for stable output.
func (e *Engine) renderAgentList() string {
	e.initBackgroundTasks()
	e.bgMu.Lock()
	ids := make([]string, 0, len(e.bgTasks))
	for id := range e.bgTasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	now := time.Now()
	lines := make([]string, 0, len(ids))
	for _, id := range ids {
		lines = append(lines, agentJobLine(e.bgTasks[id], now))
	}
	e.bgMu.Unlock()
	if len(lines) == 0 {
		return "No background sub-agent jobs."
	}
	return strings.Join(lines, "\n")
}

// --- list_agents -------------------------------------------------------------

// listAgentsToolSpec returns the tool definition for enumerating background jobs.
func listAgentsToolSpec(zh bool) ModelTool {
	desc := "List the background sub-agent jobs of this session with their status (running / done / awaiting_user), agent role, elapsed time and goal. Call it before dispatching more work to see what is already running."
	if zh {
		desc = "列出本会话的后台子代理任务及其状态（running / done / awaiting_user）、角色、已运行时长与目标。派发新任务前先用它查看已在运行的任务。"
	}
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        ListAgentsToolName,
			Description: desc,
			Parameters:  json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		},
	}
}

// processListAgentsCalls answers every list_agents call with the current table.
func (e *Engine) processListAgentsCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != ListAgentsToolName {
			continue
		}
		msgs = append(msgs, agentToolMessage(call.ID, e.renderAgentList()))
	}
	return msgs
}

// --- wait_agent --------------------------------------------------------------

// wait_agent timeout bounds. The tool takes a timeout_ms argument; the model
// can neither wait forever nor spin in a zero-length wait.
const (
	defaultAgentWaitMS    = 30_000
	minAgentWaitMS        = 100
	maxAgentWaitMS        = 600_000
	agentWaitPollInterval = 250 * time.Millisecond
)

// WaitAgentParams is the argument schema of the wait_agent tool call.
// JobIDs empty means "every running job".
type WaitAgentParams struct {
	JobIDs    []string `json:"job_ids,omitempty"`
	TimeoutMS int      `json:"timeout_ms,omitempty"`
}

// clampAgentWaitMS bounds a caller-supplied timeout, defaulting when unset.
func clampAgentWaitMS(ms int) int {
	if ms <= 0 {
		return defaultAgentWaitMS
	}
	if ms < minAgentWaitMS {
		return minAgentWaitMS
	}
	if ms > maxAgentWaitMS {
		return maxAgentWaitMS
	}
	return ms
}

// waitAgentToolSpec returns the tool definition for waiting on background jobs.
func waitAgentToolSpec(zh bool) ModelTool {
	desc := "Block until one of the given background sub-agent jobs finishes, the timeout elapses, or the turn is cancelled — then report each job's status. OBSERVES only: it never consumes a result, so collect it afterwards with agent_poll(job_id). Omit job_ids to wait on every running job."
	if zh {
		desc = "阻塞等待，直到指定的后台子代理任务之一完成、超时或本轮被取消，然后报告各任务状态。只观测、不消费结果——之后仍要用 agent_poll(job_id) 取结果。省略 job_ids 表示等待所有运行中的任务。"
	}
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        WaitAgentToolName,
			Description: desc,
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_ids": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Job handles to wait for (e.g. bg-1). Omit to wait on every running job."
					},
					"timeout_ms": {
						"type": "integer",
						"description": "Maximum wait in milliseconds (default 30000, max 600000)."
					}
				},
				"required": []
			}`),
		},
	}
}

// processWaitAgentCalls answers every wait_agent call. It blocks the turn for up
// to the requested timeout — that is the point of the tool (the alternative is
// the model burning turns re-polling).
func (e *Engine) processWaitAgentCalls(ctx context.Context, calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != WaitAgentToolName {
			continue
		}
		var params WaitAgentParams
		if err := json.Unmarshal(call.Input, &params); err != nil {
			msgs = append(msgs, agentToolMessage(call.ID,
				`Error: wait_agent expects {"job_ids": ["bg-1", ...], "timeout_ms": <int>}`))
			continue
		}
		msgs = append(msgs, agentToolMessage(call.ID, e.waitForAgents(ctx, params)))
	}
	return msgs
}

// waitForAgents blocks until any target job settles, the timeout elapses, or
// ctx is cancelled. It polls the done signal at agentWaitPollInterval instead of
// a select over the dynamic target set: a goroutine-per-target select would leak
// one goroutine per target after a timed-out wait.
//
// A job whose sub-agent ignores cancellation, or that is dropped from the table,
// cannot hang the wait forever: ctx and the deadline are both always in play.
func (e *Engine) waitForAgents(ctx context.Context, params WaitAgentParams) string {
	timeout := time.Duration(clampAgentWaitMS(params.TimeoutMS)) * time.Millisecond

	e.initBackgroundTasks()
	e.bgMu.Lock()
	targets := e.waitTargetsLocked(params.JobIDs)
	e.bgMu.Unlock()
	if len(targets) == 0 {
		return "No matching background jobs to wait for."
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(agentWaitPollInterval)
	defer ticker.Stop()

	reason := ""
	for reason == "" {
		if e.anyTargetSettled(targets) {
			reason = "activity"
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			// Re-check before calling it a timeout: a target may have settled
			// between the last poll and the deadline, and a "timeout" header
			// above a done line would contradict itself.
			if e.anyTargetSettled(targets) {
				reason = "activity"
			} else {
				reason = "timeout"
			}
		case <-ctx.Done():
			reason = "cancelled"
		}
	}

	e.bgMu.Lock()
	var b strings.Builder
	fmt.Fprintf(&b, "wait_agent (%s):\n", reason)
	now := time.Now()
	for _, t := range targets {
		// Removed while we waited (polled, cancelled, TTL-dropped): reporting a
		// handle the model can no longer act on is worse than reporting nothing.
		if e.bgTasks[t.id] != t {
			continue
		}
		b.WriteString(agentJobLine(t, now))
		b.WriteByte('\n')
	}
	e.bgMu.Unlock()
	b.WriteString("Collect a done job's result with agent_poll(job_id).")
	return b.String()
}

// anyTargetSettled reports whether any target job reached a settled state. It
// takes bgMu itself (unlike agentJobSettled, which requires the caller to).
func (e *Engine) anyTargetSettled(targets []*bgTask) bool {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	for _, t := range targets {
		if agentJobSettled(t) {
			return true
		}
	}
	return false
}

// waitTargetsLocked selects the jobs wait_agent observes: the named ones, or
// every running job when none were named. Callers must hold bgMu.
func (e *Engine) waitTargetsLocked(ids []string) []*bgTask {
	if len(ids) == 0 {
		out := make([]*bgTask, 0, len(e.bgTasks))
		for _, t := range e.bgTasks {
			if t.state == bgStateRunning {
				out = append(out, t)
			}
		}
		return out
	}
	out := make([]*bgTask, 0, len(ids))
	for _, id := range ids {
		if t, ok := e.bgTasks[id]; ok {
			out = append(out, t)
		}
	}
	return out
}

// --- send_message ------------------------------------------------------------

// SendMessageParams is the argument schema of the send_message tool call.
type SendMessageParams struct {
	JobID   string `json:"job_id"`
	Message string `json:"message"`
}

// sendMessageToolSpec returns the tool definition for messaging a running job.
func sendMessageToolSpec(zh bool) ModelTool {
	desc := "Send a message to a RUNNING background sub-agent. It reads the message at its next step and can act on it (redirect the work, add a constraint, answer its question). Check its state with agent_poll first; for a job that is waiting for the user, use agent_resume instead."
	jobDesc := "The job handle, e.g. bg-3"
	msgDesc := "The message, phrased as an instruction or fact for the sub-agent"
	if zh {
		desc = "向正在运行的后台子代理发送一条消息。它会在下一步读到并据此行动（改方向、加约束、回答它的疑问）。先用 agent_poll 看状态；等待用户输入的任务请改用 agent_resume。"
		jobDesc = "任务句柄，例如 bg-3"
		msgDesc = "消息内容，写成给子代理的指令或事实"
	}
	params := fmt.Sprintf(`{
		"type": "object",
		"properties": {
			"job_id": {"type": "string", "description": %q},
			"message": {"type": "string", "description": %q}
		},
		"required": ["job_id", "message"]
	}`, jobDesc, msgDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        SendMessageToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// processSendMessageCalls delivers every send_message call into its target's
// inbox. A non-running or unknown job is refused with a pointer to the right
// tool, so the model can self-correct in one turn.
func (e *Engine) processSendMessageCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != SendMessageToolName {
			continue
		}
		var params SendMessageParams
		if err := json.Unmarshal(call.Input, &params); err != nil ||
			strings.TrimSpace(params.JobID) == "" || strings.TrimSpace(params.Message) == "" {
			msgs = append(msgs, agentToolMessage(call.ID,
				"Error: send_message requires a non-empty job_id and message"))
			continue
		}

		e.initBackgroundTasks()
		e.bgMu.Lock()
		task := e.bgTasks[params.JobID]
		if task == nil {
			e.bgMu.Unlock()
			msgs = append(msgs, agentToolMessage(call.ID, fmt.Sprintf(
				"Error: send_message job %q not found (already consumed or run ended).", params.JobID)))
			continue
		}
		if task.state != bgStateRunning || channelClosed(task.done) {
			state := agentJobState(task)
			e.bgMu.Unlock()
			msgs = append(msgs, agentToolMessage(call.ID, fmt.Sprintf(
				"Error: job %s is not running (%s). Use agent_poll(%s) to collect it, or agent_resume(%s, <answer>) if it is waiting for the user.",
				params.JobID, state, params.JobID, params.JobID)))
			continue
		}
		inbox := task.inbox
		e.bgMu.Unlock()

		if inbox == nil {
			msgs = append(msgs, agentToolMessage(call.ID, fmt.Sprintf(
				"Error: job %s has no message inbox (it was not started as an async job).", params.JobID)))
			continue
		}
		select {
		case inbox <- params.Message:
			msgs = append(msgs, agentToolMessage(call.ID, fmt.Sprintf(
				"Message delivered to %s; it reads it at its next step.", params.JobID)))
		default:
			msgs = append(msgs, agentToolMessage(call.ID, fmt.Sprintf(
				"Error: job %s's inbox is full — it has not consumed the previous messages yet. Poll it before sending more.", params.JobID)))
		}
	}
	return msgs
}

// --- interrupt_agent / close_agent -------------------------------------------

// AgentJobParams is the argument schema shared by interrupt_agent and
// close_agent: both name a single job handle.
type AgentJobParams struct {
	JobID string `json:"job_id"`
}

// interruptAgentToolSpec returns the tool definition for soft-stopping a job.
func interruptAgentToolSpec(zh bool) ModelTool {
	desc := "Interrupt a running background sub-agent. It stops at its next safe point with finish_reason=cancelled; the job stays collectable, so fetch the partial result with agent_poll(job_id). Use it when the sub-agent has gone off track and letting it run would waste budget."
	jobDesc := "The job handle, e.g. bg-3"
	if zh {
		desc = "中断正在运行的后台子代理。它会在下一个安全点停下（finish_reason=cancelled），任务仍可收集——用 agent_poll(job_id) 取部分结果。当子代理跑偏、继续运行会浪费预算时使用。"
		jobDesc = "任务句柄，例如 bg-3"
	}
	params := fmt.Sprintf(`{
		"type": "object",
		"properties": {"job_id": {"type": "string", "description": %q}},
		"required": ["job_id"]
	}`, jobDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        InterruptAgentToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// closeAgentToolSpec returns the tool definition for permanently dropping a job.
func closeAgentToolSpec(zh bool) ModelTool {
	desc := "Close a background sub-agent and every job below it, permanently. Unlike interrupt_agent, the job is removed and its result can no longer be collected — use it when the work is no longer wanted."
	jobDesc := "The job handle, e.g. bg-3"
	if zh {
		desc = "彻底关闭一个后台子代理及其下级所有任务。与 interrupt_agent 不同，任务会被移除，结果不再可取——当这项工作已经不需要时使用。"
		jobDesc = "任务句柄，例如 bg-3"
	}
	params := fmt.Sprintf(`{
		"type": "object",
		"properties": {"job_id": {"type": "string", "description": %q}},
		"required": ["job_id"]
	}`, jobDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        CloseAgentToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// processInterruptAgentCalls answers every interrupt_agent call.
func (e *Engine) processInterruptAgentCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != InterruptAgentToolName {
			continue
		}
		var params AgentJobParams
		if err := json.Unmarshal(call.Input, &params); err != nil || strings.TrimSpace(params.JobID) == "" {
			msgs = append(msgs, agentToolMessage(call.ID, "Error: interrupt_agent requires a non-empty job_id"))
			continue
		}
		msgs = append(msgs, agentToolMessage(call.ID, e.interruptJob(params.JobID)))
	}
	return msgs
}

// interruptJob cancels a running job's context. The entry STAYS in the table so
// the partial result is still collectable — that is the difference from
// closeAgentJob.
func (e *Engine) interruptJob(jobID string) string {
	e.initBackgroundTasks()
	e.bgMu.Lock()
	task, ok := e.bgTasks[jobID]
	if !ok {
		e.bgMu.Unlock()
		return fmt.Sprintf("Error: interrupt_agent job %q not found.", jobID)
	}
	if task.state != bgStateRunning || channelClosed(task.done) {
		state := agentJobState(task)
		e.bgMu.Unlock()
		return fmt.Sprintf("Job %s is not running (%s) — nothing to interrupt.", jobID, state)
	}
	cancel := task.cancel
	e.bgMu.Unlock()
	if cancel == nil {
		return fmt.Sprintf("Job %s has no in-flight request to interrupt.", jobID)
	}
	cancel()
	return fmt.Sprintf("Job %s interrupted; it stops at its next safe point. Collect the partial result with agent_poll(%s).", jobID, jobID)
}

// processCloseAgentCalls answers every close_agent call.
func (e *Engine) processCloseAgentCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != CloseAgentToolName {
			continue
		}
		var params AgentJobParams
		if err := json.Unmarshal(call.Input, &params); err != nil || strings.TrimSpace(params.JobID) == "" {
			msgs = append(msgs, agentToolMessage(call.ID, "Error: close_agent requires a non-empty job_id"))
			continue
		}
		msgs = append(msgs, agentToolMessage(call.ID, e.closeAgentJob(params.JobID)))
	}
	return msgs
}

// closeAgentJob cancels and forgets a job and every job below it. Cancellation
// happens AFTER the table edit (cancel funcs are captured first) so a run
// finishing during the edit cannot resurrect an entry that was just dropped.
func (e *Engine) closeAgentJob(jobID string) string {
	e.initBackgroundTasks()
	e.bgMu.Lock()
	if _, ok := e.bgTasks[jobID]; !ok {
		e.bgMu.Unlock()
		return fmt.Sprintf("Error: close_agent job %q not found.", jobID)
	}
	ids := e.descendantJobIDsLocked(jobID)
	cancels := make([]context.CancelFunc, 0, len(ids))
	suspended := 0
	for _, id := range ids {
		t := e.bgTasks[id]
		if t == nil {
			continue
		}
		if t.state == bgStateAwaitingUser {
			suspended++
		}
		if t.cancel != nil {
			cancels = append(cancels, t.cancel)
		}
		if t.done != nil && !channelClosed(t.done) {
			close(t.done) // release any waiter
		}
		delete(e.bgTasks, id)
	}
	e.bgMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	msg := fmt.Sprintf("Closed %d job(s): %s.", len(ids), strings.Join(ids, ", "))
	if suspended > 0 {
		msg += fmt.Sprintf(" %d suspended job(s) were dropped — their pending questions can no longer be answered.", suspended)
	}
	return msg
}

// descendantJobIDsLocked returns id plus every job below it. The delegation
// link is stored on the PARENT — childRunID names the pending child (that is
// what finishResumedJob reverse-looks-up to find a parent) — so the walk follows
// the chain downward by map lookup. Bounded by the chain length.
//
// Callers must hold bgMu.
func (e *Engine) descendantJobIDsLocked(id string) []string {
	out := []string{id}
	for i := 0; i < len(out); i++ {
		t := e.bgTasks[out[i]]
		if t == nil || t.childRunID == "" {
			continue
		}
		if _, ok := e.bgTasks[t.childRunID]; !ok || containsString(out, t.childRunID) {
			continue
		}
		out = append(out, t.childRunID)
	}
	return out
}
