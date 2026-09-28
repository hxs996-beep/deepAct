package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// runHandoff is the shared delegation core used by both backends.
// opts carries per-caller differences: how to resolve the agent registry,
// how to inject parent context, whether to accumulate usage, and how to
// localize output.
type handoffOptions struct {
	// resolve returns the target agent, or an error digest if unavailable.
	resolve func(AgentID) (Agent, error)
	// injectParentCtx optionally appends parent working context to params.Context.
	injectParentCtx func(params *HandoffToAgentParams)
	// accumulate reports sub-agent usage into the parent's counter.
	accumulate func(*ModelUsage)
	// zh localizes the result digest.
	zh bool
	// onUsage reports sub-agent usage to the parent (e.g. as a "usage"
	// ProgressEvent for the UI). Called alongside accumulate when both set.
	onUsage func(*ModelUsage)
	// depth is the depth of the new sub-agent run (0 = first level).
	depth int
	// userLang is the session language (see UserLanguage).
	userLang UserLanguage
	// register hands a suspended run to the engine's job table and returns its
	// handle ("" = not registered: no registrar injected, or the cap is full).
	// Called BEFORE the digest is formatted so the handle can appear in it.
	// nil (bare runner, tests) = suspensions are not registered, so the run
	// behaves exactly as it does today.
	register func(*SuspendedRun, AgentID, string) string
}

func runHandoff(ctx context.Context, call ToolCallRequest, opts handoffOptions) ToolResult {
	var params HandoffToAgentParams
	if err := json.Unmarshal(call.Input, &params); err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("invalid handoff params: %v", err),
		}
	}

	agent, err := opts.resolve(AgentID(params.Agent))
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("agent not found: %s - %v", params.Agent, err),
		}
	}

	if opts.injectParentCtx != nil {
		opts.injectParentCtx(&params)
	}

	handoff := Handoff{
		Agent:          AgentID(params.Agent),
		Goal:           params.Goal,
		Context:        params.Context,
		Tools:          params.Tools,
		Constraints:    params.Constraints,
		ExpectedOutput: params.ExpectedOutput,
		Persona:        params.Persona,
		Depth:          opts.depth,
		UserLanguage:   opts.userLang,
	}

	result, err := agent.Run(ctx, handoff)
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("agent error: %v", err),
		}
	}

	if result.Usage != nil {
		if opts.accumulate != nil {
			opts.accumulate(result.Usage)
		}
		if opts.onUsage != nil {
			opts.onUsage(result.Usage)
		}
	}

	// Register a suspension BEFORE formatting the digest: the handle has to be
	// rendered into it so the parent knows what to resume. Registration happens
	// here (not at depth 0) because the nested backend has no Engine reference.
	runID := ""
	if result.Suspended != nil && opts.register != nil {
		runID = opts.register(result.Suspended, AgentID(params.Agent), params.Goal)
	}
	// Only the top-level delegation exposes its handle: deeper handles must never
	// reach the model (a nested digest is inlined into its parent's Summary, so
	// rendering one there would leak the whole chain).
	if opts.depth == 0 {
		result.RunID = runID
	}

	status := "ok"
	if result.BlockedBy == "cancelled" {
		status = "cancelled"
	}
	return ToolResult{
		ToolCallID:   call.ID,
		ToolName:     HandoffToolName,
		Status:       status,
		Digest:       formatHandoffResult(result, opts.zh),
		FinishReason: result.FinishReason,
		Questions:    result.Questions,
		RunID:        runID,
	}
}

// RunSubAgent implements the main-agent backend for SubAgentTool: depth 0.
func (e *Engine) RunSubAgent(ctx context.Context, params HandoffToAgentParams, depth int, userLang UserLanguage) (ToolResult, error) {
	if e.agents == nil {
		return ToolResult{Status: "error", Digest: "no agent registry configured"}, nil
	}
	if params.Async && depth == 0 {
		return e.dispatchAsync(ctx, params, userLang)
	}
	call := ToolCallRequest{Name: HandoffToolName, Input: mustJSON(params)}
	// The engine's session-locked language is authoritative for the sync path;
	// the caller-supplied userLang only feeds the async dispatch above.
	userLang = UserLanguageFor(e.isChinese)
	res := runHandoff(ctx, call, handoffOptions{
		resolve: func(id AgentID) (Agent, error) { return e.agents.Get(id) },
		injectParentCtx: func(p *HandoffToAgentParams) {
			if e.state != nil {
				*p = injectMainAgentContext(*p, e.state)
			}
		},
		accumulate: e.accumulateUsage,
		onUsage: func(u *ModelUsage) {
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{Type: "usage", Usage: u})
			}
		},
		zh:       e.isChinese,
		depth:    depth,
		userLang: userLang,
		register: e.RegisterSuspended,
	})
	// Bubble up sub-agent questions into the pending ask_user seam so the
	// existing awaiting_user / Options UI path presents them. RunID ties the
	// question to the job that must be resumed with the answer; empty means the
	// run was not registered (cap full), i.e. the parent re-delegates instead.
	if len(res.Questions) > 0 {
		e.pushAskUser(&AskUserRequest{Question: res.Questions[0], RunID: res.RunID})
	}
	return res, nil
}

// defaultMaxOutstandingAsyncSubAgents caps dispatched-but-uncollected async
// sub-agent jobs when [context].max_outstanding_async_subagents is unset.
const defaultMaxOutstandingAsyncSubAgents = 8

// maxOutstandingAsyncSubAgents resolves the effective outstanding cap; 0 or
// negative config means the default. Resolved engine-side so a bare Engine
// (tests, embeddings) gets the cap without wiring defaults.
func (e *Engine) maxOutstandingAsyncSubAgents() int {
	if n := e.config.MaxOutstandingAsyncSubAgents; n > 0 {
		return n
	}
	return defaultMaxOutstandingAsyncSubAgents
}

// maxSuspendedSubAgentsEff resolves the effective suspension cap from
// [context].max_suspended_subagents; 0 or negative means the default. Read from
// the config (like the async outstanding cap) so there is a single source of
// truth and a bare Engine still gets a cap.
func (e *Engine) maxSuspendedSubAgentsEff() int {
	if e.config.MaxSuspendedSubAgents > 0 {
		return e.config.MaxSuspendedSubAgents
	}
	return defaultMaxSuspendedSubAgents
}

// RegisterSuspended registers a suspended run as an awaiting_user job and
// returns its handle. "" means NOT registered — the suspension cap is full — and
// the caller then falls back to today's behaviour: the question still bubbles up
// and the parent re-delegates instead of resuming.
//
// Exported because it is the wiring seam for SubAgentRunner.SetSuspendedRegistrar:
// the nested backend lives in the same package but is assembled in cmd/run.go,
// where an unexported method is not reachable.
//
// The result channel is created here on purpose: a suspended entry holds no
// goroutine, and a nil channel always takes the default branch of a select, so
// without it the resumed run's final result would be dropped and agent_poll
// would report "still running" forever.
func (e *Engine) RegisterSuspended(s *SuspendedRun, agent AgentID, goal string) string {
	e.initBackgroundTasks()
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	if e.countByStateLocked(bgStateAwaitingUser) >= e.maxSuspendedSubAgentsEff() {
		loopLog.Printf("suspend cap (%d) reached; run %s ends without a resumable entry", e.maxSuspendedSubAgentsEff(), agent)
		return ""
	}
	e.bgSeq++
	id := fmt.Sprintf("bg-%d", e.bgSeq)
	// ctx/cancel stay nil: a suspended entry holds no goroutine and no request.
	// They are set at RESUME time from the ctx of the Run that calls
	// agent_resume, so the resumed run is cancellable and — like every async
	// job — never outlives that Run.
	e.bgTasks[id] = &bgTask{
		id:         id,
		agent:      agent,
		goal:       goal,
		state:      bgStateAwaitingUser,
		suspended:  s,
		childRunID: s.ChildRunID,
		startAt:    time.Now(),
		result:     make(chan *HandoffResult, 1),
	}
	return id
}

// suspendExistingJob flips an in-flight (async) job into a suspended one. It
// keeps the SAME job id — the dispatcher already handed that handle to the
// model, so allocating a second one would strand the original entry. Reports
// false when the suspension cap is full or the entry is gone; the caller then
// delivers the question as a final result rather than losing it.
func (e *Engine) suspendExistingJob(jobID string, s *SuspendedRun) bool {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	if e.countByStateLocked(bgStateAwaitingUser) >= e.maxSuspendedSubAgentsEff() {
		return false
	}
	t, ok := e.bgTasks[jobID]
	if !ok {
		return false
	}
	t.state = bgStateAwaitingUser
	t.suspended = s
	t.childRunID = s.ChildRunID
	t.startAt = time.Now() // TTL counts from the suspension, not from dispatch
	t.ctx, t.cancel = nil, nil
	return true
}

// dispatchAsync starts the sub-agent in the background and returns
// immediately with a job_id. The delegating agent polls the result later via
// agent_poll(job_id). The background task is cancelled at Run exit — it never
// outlives the Run that started it.
func (e *Engine) dispatchAsync(ctx context.Context, params HandoffToAgentParams, userLang UserLanguage) (ToolResult, error) {
	agent, err := e.agents.Get(AgentID(params.Agent))
	if err != nil {
		return ToolResult{Status: "error", Digest: fmt.Sprintf("agent not found: %s - %v", params.Agent, err)}, nil
	}
	handoff := Handoff{
		Agent:          AgentID(params.Agent),
		Goal:           params.Goal,
		Context:        params.Context,
		Tools:          params.Tools,
		Constraints:    params.Constraints,
		ExpectedOutput: params.ExpectedOutput,
		Persona:        params.Persona,
		Depth:          0,
		UserLanguage:   userLang,
	}
	if e.state != nil {
		params = injectMainAgentContext(params, e.state)
		handoff.Context = params.Context
	}

	e.initBackgroundTasks()
	e.bgMu.Lock()
	// Outstanding cap: async jobs hold a slot until agent_poll consumes their
	// done result (turn.go deletes on poll). fail-loud, not blocking — async
	// semantics require an immediate return; the digest points the model at
	// collecting existing results first.
	// Count only jobs with work in flight: suspended (awaiting_user) entries hold
	// no goroutine and no LLM request, so they must not consume an async slot.
	if n := e.maxOutstandingAsyncSubAgents(); e.countByStateLocked(bgStateRunning) >= n {
		e.bgMu.Unlock()
		return ToolResult{
			ToolName: HandoffToolName,
			Status:   "error",
			Digest:   fmt.Sprintf("background sub-agent limit reached (%d outstanding). Poll existing results with agent_poll first.", n),
		}, nil
	}
	e.bgSeq++
	jobID := fmt.Sprintf("bg-%d", e.bgSeq)
	bgCtx, cancel := context.WithCancel(ctx)
	task := &bgTask{
		id:      jobID,
		agent:   AgentID(params.Agent),
		goal:    params.Goal,
		ctx:     bgCtx,
		cancel:  cancel,
		result:  make(chan *HandoffResult, 1),
		startAt: time.Now(),
		state:   bgStateRunning,
	}
	e.bgTasks[jobID] = task
	e.bgMu.Unlock()

	// agent_start event for UI (same as synchronous handoff).
	if e.config.OnProgress != nil {
		name := params.Agent
		if name == "" {
			name = "sub"
		}
		e.config.OnProgress(ProgressEvent{Type: "agent_start", Name: name, Detail: params.Goal})
	}

	go func() {
		result, runErr := agent.Run(bgCtx, handoff)
		if result == nil {
			summary := "(no result)"
			reason := HandoffReasonError
			if runErr != nil {
				summary = "(sub-agent error: " + runErr.Error() + ")"
			}
			result = &HandoffResult{
				Summary:      summary,
				Blocked:      true,
				BlockedBy:    "sub_agent_error",
				FinishReason: reason,
			}
		}
		if result.Usage != nil {
			e.accumulateUsage(result.Usage)
		}
		// A background run that stopped to ask the user SUSPENDS: the job stays
		// in the table as awaiting_user and is answered later with
		// agent_resume. The channel only ever carries a FINAL result — pushing
		// the question here would make agent_poll report the job as done and the
		// resumed run's real result would then be dropped by select/default.
		// agent_done is withheld for the same reason: the job is not done, it is
		// waiting. The question reaches the model via the pinned job summary and
		// agent_poll instead of the ask_user UI path, because this Run may
		// already be over.
		if result.Suspended != nil && e.suspendExistingJob(task.id, result.Suspended) {
			loopLog.Printf("async job %s suspended on a question; answer with agent_resume(%s, ...)", task.id, task.id)
			return
		}
		select {
		case task.result <- result:
		default:
		}
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{
				Type:   "agent_done",
				Name:   string(task.agent),
				Detail: result.Summary,
			})
		}
	}()

	return ToolResult{
		ToolCallID:   "",
		ToolName:     HandoffToolName,
		Status:       "ok",
		Digest:       fmt.Sprintf("Dispatched async job %s (%s): %s. Use agent_poll(%s) to check the result.", jobID, params.Agent, params.Goal, jobID),
		FinishReason: HandoffReasonAsyncRunning,
	}, nil
}

// RunSubAgent implements the nested-agent backend for SubAgentTool: depth > 0.
func (r *SubAgentRunner) RunSubAgent(ctx context.Context, params HandoffToAgentParams, depth int, userLang UserLanguage) (ToolResult, error) {
	if r.registry == nil {
		return ToolResult{Status: "error", Digest: "no agent registry configured"}, nil
	}
	call := ToolCallRequest{Name: HandoffToolName, Input: mustJSON(params)}
	res := runHandoff(ctx, call, handoffOptions{
		resolve:    func(id AgentID) (Agent, error) { return r.registry.Get(id) },
		accumulate: nil,
		zh:         zhFromLang(userLang),
		depth:      depth,
		userLang:   userLang,
		register:   r.register,
	})
	return res, nil
}

// injectMainAgentContext appends the main agent's working set, memory markers,
// and modified files to the handoff context (moved from Engine.executeHandoff).
func injectMainAgentContext(params HandoffToAgentParams, state *TaskState) HandoffToAgentParams {
	extra := mainAgentContextText(state)
	if extra == "" {
		return params
	}
	if params.Context != "" {
		params.Context = params.Context + extra
	} else {
		params.Context = extra
	}
	return params
}

// mainAgentContextText builds the working-set/markers/modified-files block.
func mainAgentContextText(state *TaskState) string {
	var sb strings.Builder
	if len(state.WorkingSet.Files) > 0 {
		sb.WriteString("\n## Main Agent Context (Review Starting Point)\n")
		sb.WriteString("The main agent examined these files. Re-examine them from your own perspective:\n")
		for _, f := range state.WorkingSet.Files {
			sb.WriteString(fmt.Sprintf("- %s (%s)\n", f.Path, f.Notes))
		}
	}
	if len(state.MemoryMarkers) > 0 {
		sb.WriteString("\nKey findings from the main agent (review for blind spots):\n")
		for _, m := range state.MemoryMarkers {
			sb.WriteString(fmt.Sprintf("  • %s\n", m))
		}
	}
	if len(state.ModifiedFiles) > 0 {
		sb.WriteString("\nFiles modified so far:\n")
		for _, f := range state.ModifiedFiles {
			sb.WriteString(fmt.Sprintf("- %s\n", f))
		}
	}
	return sb.String()
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
