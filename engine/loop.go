package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	dlog "github.com/deepact/deepact/internal/log"
	"github.com/deepact/deepact/skill"
)

var loopLog = dlog.New("[loop] ")

const (
	// DefaultMaxOutputTokens is the per-turn LLM completion cap when the config
	// doesn't override it. DeepSeek's 1M context window supports large
	// completions, so 128K lets the model emit full code edits in one turn
	// rather than being cut off and forced to continue piecemeal (the old 8K
	// cap was set to save tokens / push the model to answer early, but it
	// truncated real code output).
	DefaultMaxOutputTokens = 128 * 1024
)

// SubAgentBudget is the delegation-tree budget surface the Engine owns across a
// Run: it clears the Run-scoped aggregate at Run start and reports the spend for
// the near-limit notice. *SubAgentRunner implements it; cmd/run.go injects it
// through EngineDeps. nil = no aggregate (tests, embeddings) and the notice
// stays silent.
type SubAgentBudget interface {
	StartRun()
	Spent() int64
	Budget() int64
	ClaimBudgetNotice() bool
}

type EngineDeps struct {
	Model      ModelClient
	Tools      ToolExecutor
	Context    ContextBuilder
	Compressor Compressor
	Session    SessionStore
	Memory     MemoryStore
	Agents     *AgentRegistry
	// SubAgents, when set, is the shared sub-agent budget surface. Optional:
	// nil means the Engine neither resets nor reports the Run aggregate.
	SubAgents   SubAgentBudget
	Skills      *skill.Registry
	MCPManagers []io.Closer // MCP server connections to close on shutdown

	// AfterEngine, when set, is called at the end of NewEngine with the new
	// Engine instance. Used by cmd/run.go to register tools that need a live
	// Engine reference (e.g. the SubAgentTool backends).
	AfterEngine func(*Engine)
}

type Engine struct {
	model      ModelClient
	tools      ToolExecutor
	context    ContextBuilder
	compressor Compressor
	session    SessionStore
	memory     MemoryStore
	agents     *AgentRegistry
	// subBudget is the shared delegation-tree budget (nil when not wired).
	subBudget SubAgentBudget
	skills    *skill.Registry
	config    EngineConfig
	state     *TaskState
	history   []Message
	// contextRetryDone caps the reactive context-length retry to once per Run:
	// a 400 after the pre-send estimate is rare, and retrying forever would just
	// re-trim and re-fail (see executeTurn).
	contextRetryDone bool
	// toolExposureLogged guards the one-shot tool-array diagnostic. The tool set
	// is fixed for the process, so the exposure line is logged once (see
	// logToolExposureOnce) rather than every turn.
	toolExposureLogged bool
	steerMu            sync.Mutex
	steerQueue         []string
	guards             *GuardSystem
	// progressLoop is the single no-progress breaker — the only loop guard that
	// can still end a Run. Per-target repetition is no longer blocked: it is
	// reported to the model as a fact (see noteRepeats), so the model decides
	// whether to change approach or conclude.
	progressLoop *LoopTracker
	// progressKeys tracks "new information" keys already seen this Run
	// ("read:path::scope", "grep:<pattern>:<path>", "glob:<pattern>:<path>").
	// A novel key counts as progress for the progress LoopTracker
	// (progressLoop), so legitimate investigation — reading or searching new
	// content — is not mistaken for a no-progress loop. Reset each Run.
	progressKeys map[string]bool
	// observedAgentStates collects the async sub-agent status observations made
	// during the CURRENT turn as novel-info keys ("agent:<job>::<state>"), for
	// the progress signal at the end of executeTurn. A job seen for the first
	// time, or moving to a new state (running→done/awaiting_user), is new
	// information = progress; re-observing the same unchanged state is not.
	// Rebuilt every turn (see executeTurn).
	observedAgentStates []string
	// repeatCounts counts per-target tool invocations within the current Run
	// ("read:path::scope", "edit:path#sig", "grep:pattern:path", ...). A repeat
	// is reported to the model as a fact alongside the tool result — it never
	// blocks the call or ends the Run. Reset each Run.
	repeatCounts map[string]int
	evalStore    EvalStore

	// pendingPinnedMessages holds messages (e.g., skill loads) that should
	// be appended at the END of the assembled messages array for the current
	// Run() call, rather than mixed into e.history. This preserves the stable
	// prefix cache across turns — history only grows with actual conversation.
	pendingPinnedMessages []string

	// pendingAskUser holds the question the agent asked the user via ask_user.
	// Non-nil means the engine is awaiting the user's response — with Options
	// the popup shows the raw options for the user to choose, without Options
	// the question is presented via the awaiting_user Blocked path for free input.
	// NOT reset at Run start — it must survive until the next Run's
	// handleConfirmCommand (with options) or the free-input path reads it.
	// Cleared once consumed.
	// pendingAskUser is a QUEUE of questions awaiting the user: parallel
	// sub-agents can ask at the same time and a single slot made them overwrite
	// each other. The head is the one currently presented; each entry carries
	// the job handle its answer goes back to (RunID).
	pendingAskUser []*AskUserRequest
	// askUserMu guards pendingAskUser: parallel sub-agent handoffs can
	// bubble up questions concurrently, so the write must be serialized.
	askUserMu sync.Mutex

	// Background async sub-agent tasks (handoff_to_agent async:true).
	// bgMu guards bgTasks and bgSeq. Tasks are cancelled at Run exit —
	// they never outlive the Run that started them.
	bgMu    sync.Mutex
	bgSeq   int
	bgTasks map[string]*bgTask

	// Per-Run efficiency tracking
	runStartAt       time.Time
	runUsageAccum    ModelUsage
	usageMu          sync.Mutex // protects runUsageAccum from concurrent sub-agent goroutines
	runToolCallCount int
	// stopHookActive is true when the current turn was triggered by a stop
	// hook blocking the previous turn's exit (mirrors Claude Code's stopHookActive).
	stopHookActive bool
	// stopHookRetryCount tracks consecutive stop-hook-triggered continuations.
	// Reset to 0 when tools are called or at the start of each Run().
	stopHookRetryCount int
	// stopHooks are checked when the model outputs text without tool calls.
	stopHooks []StopHook
	// runStartHistoryLen is the index in e.history where the current Run()'s
	// turn loop began. buildRunSummary only considers assistant messages at
	// or after this index, so a stale narration from a prior run cannot leak
	// into this run's summary when the model emits bare tool calls (empty
	// Content) and never produces a final text body.
	runStartHistoryLen int
	// persistedCount records how many history messages have been written as
	// message events. persistHistory only writes history[persistedCount:];
	// it resets to 0 when compression replaces history (out-of-bounds guard).
	persistedCount int
	runErrorCount  int

	// lastTodos is the newest step-list snapshot the model reported through
	// todo_write during this Run (see processTodoWriteCalls); todoNudgeSent
	// records that this Run's one-shot completion nudge has been spent. Together
	// they let task_complete refuse a stale panel: a Run must not end while the
	// panel it leaves on screen still says a step is in progress. Both are reset
	// at Run start.
	lastTodos     []TodoItem
	todoNudgeSent bool

	// memoryLoaded guards lazy persistent-memory loading. Memory is restored
	// only on /resume (SetHistory), never at startup, so a fresh session
	// starts clean of cross-task markers/decisions.
	memoryLoaded bool
}

// Job states. A job is either running (a goroutine + an LLM request in flight)
// or suspended on ask_user (no goroutine, no in-flight work — it is waiting for
// the user's answer). Suspended entries are the only ones that survive the end
// of a Run: dropping them would lose a question the user still owes an answer.
const (
	bgStateRunning      = "running"
	bgStateAwaitingUser = "awaiting_user"
)

// defaultMaxSuspendedSubAgents caps suspended jobs when
// [context].max_suspended_subagents is unset. Suspended entries each retain a
// whole history (worst case ~1M tokens), so the table must stay small.
const defaultMaxSuspendedSubAgents = 4

// inboxBufferSize bounds how many send_message calls may queue for one running
// sub-agent before the sender is told to back off. Small on purpose: messages
// are steered into the next iteration, not accumulated.
const inboxBufferSize = 8

// bgTask is one background sub-agent job: an async run started via
// handoff_to_agent(async:true), or a run suspended on ask_user.
// A running entry lives only for the current Run() — the Run's exit path
// cancels it and drops any un-polled result. A suspended entry outlives the Run
// (it holds no goroutine and no request) until its answer arrives.
type bgTask struct {
	id    string // "bg-<seq>"
	agent AgentID
	goal  string
	// step is the TodoItem.ID this job executes ("" = not linked to a step).
	// Stored here so every terminal event can report the same link the
	// dispatch reported, without re-parsing the original tool call.
	step    string
	ctx     context.Context
	cancel  context.CancelFunc
	result  chan *HandoffResult // 容量1：完成/错误；挂起条目在登记时即创建（I9）
	startAt time.Time
	// state is bgStateRunning or bgStateAwaitingUser. Every real construction
	// site sets it explicitly. Checks that must recognise a suspended entry
	// compare against bgStateAwaitingUser (never against "not running"), so an
	// entry built without a state behaves exactly as it does today.
	state string
	// suspended holds the resumable state while state == bgStateAwaitingUser.
	suspended *SuspendedRun
	// childRunID is the job id of the child whose question caused this
	// suspension (empty when this run asked the user itself). The parent entry
	// is found by reverse lookup on it — no parent field is needed.
	childRunID string
	// done is closed exactly once when the job publishes a terminal result.
	// It is a SIGNAL, not a channel to consume: several waiters and the
	// list/wait renderers can all observe completion without stealing the
	// result, which stays in `result` for agent_poll to collect.
	done chan struct{}
	// inbox carries mid-run messages injected by send_message. The sub-agent
	// run drains it at the top of each iteration (see drainInbox). nil for
	// sync/nested runs, which have no job handle to address.
	inbox chan string
	// resultBrief is a one-line digest of the terminal result, set when the job
	// finishes, so list_agents / wait_agent can show what a done job produced
	// without consuming its result channel.
	resultBrief string
}

// initBackgroundTasks lazily creates the background task map. Safe to call
// multiple times. It must NOT clear entries: suspended jobs outlive the Run
// that created them.
func (e *Engine) initBackgroundTasks() {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	if e.bgTasks == nil {
		e.bgTasks = make(map[string]*bgTask)
	}
}

// cancelBackgroundTasks drops the Run's in-flight background work: every
// running task is cancelled and removed, so no sub-agent goroutine or LLM
// request outlives the Run. Un-polled results are dropped (the UI already
// showed their progress via agent_done events).
//
// Suspended (awaiting_user) entries are KEPT: they hold no goroutine and no
// in-flight request — they are a question the user still owes an answer to, and
// that answer arrives in a later Run. Dropping them would silently lose it.
func (e *Engine) cancelBackgroundTasks() {
	e.bgMu.Lock()
	var running []*bgTask
	for id, t := range e.bgTasks {
		if t.state == bgStateAwaitingUser {
			continue
		}
		running = append(running, t)
		delete(e.bgTasks, id)
	}
	e.bgMu.Unlock()
	for _, t := range running {
		if t.cancel != nil {
			t.cancel()
		}
		loopLog.Printf("background task %s (%s) cancelled at run end; result dropped", t.id, t.agent)
	}
}

// countByStateLocked counts entries in one state. Callers must hold bgMu —
// the registration path already does, and a self-locking wrapper there would
// deadlock.
func (e *Engine) countByStateLocked(state string) int {
	n := 0
	for _, t := range e.bgTasks {
		if t.state == state {
			n++
		}
	}
	return n
}

// inFlightCount counts jobs with work in flight (async outstanding). Suspended
// entries are excluded — they hold no goroutine and no LLM request, so counting
// them would let a waiting run consume an async slot.
func (e *Engine) inFlightCount() int {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	return e.countByStateLocked(bgStateRunning)
}

// finishBackgroundJob publishes a terminal result for a background job: it
// marks the result available (done closed) and records a short brief, then
// notifies the UI. The entry STAYS in the table until agent_poll consumes it —
// that preserves the outstanding-cap semantics (a slot frees on collection, not
// on completion) and lets list/wait still observe the finished job.
//
// done is closed under bgMu with a closed-check so a double finish (e.g. a
// cancellation racing a normal return) can never panic.
func (e *Engine) finishBackgroundJob(t *bgTask, result *HandoffResult) {
	if t == nil {
		loopLog.Printf("finishBackgroundJob: ignoring a nil job")
		return
	}
	if result == nil {
		loopLog.Printf("finishBackgroundJob: ignoring a nil result for job %s", t.id)
		return
	}
	e.bgMu.Lock()
	if t.done != nil {
		select {
		case <-t.done:
			e.bgMu.Unlock()
			return // already finished
		default:
		}
	}
	// Delivery, brief and the done signal are published under ONE lock and in
	// the order consumers rely on: the result is in the channel before done
	// closes, so a waiter woken by done always finds the result readable. The
	// send cannot block — result has capacity 1 and this runs at most once per
	// job (the closed-check above).
	select {
	case t.result <- result:
	default:
	}
	t.resultBrief = briefDigest(result.Summary)
	if t.done != nil {
		close(t.done)
	}
	e.bgMu.Unlock()

	if e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{
			Type: "agent_done", Name: string(t.agent), Detail: result.Summary,
			RunID: t.id, Step: t.step, Status: handoffResultStatus(result),
		})
	}
}

// handoffResultStatus maps a finished background result to the status the UI
// shows. Cancellation wins over failure, and a blocked run is a failure — a
// finished run must never render as a success when it did not deliver.
func handoffResultStatus(r *HandoffResult) string {
	switch {
	case r == nil:
		return "error"
	case r.BlockedBy == "cancelled":
		return "cancelled"
	case r.Blocked || r.FinishReason == HandoffReasonError:
		return "error"
	default:
		return "ok"
	}
}

// suspendedCount counts jobs waiting for the user's answer.
func (e *Engine) suspendedCount() int {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	return e.countByStateLocked(bgStateAwaitingUser)
}

func NewEngine(cfg EngineConfig, deps EngineDeps) *Engine {
	guard := &GuardSystem{
		scope: NewScopeGuard(),
	}
	guard.scope.Configure(ApprovalSettings{
		Mode:      cfg.ApprovalMode,
		Rules:     cfg.ApprovalRules,
		Learned:   cfg.LearnedApprovals,
		OnLearned: cfg.OnApprovalLearned,
	})
	e := &Engine{
		model:        deps.Model,
		tools:        deps.Tools,
		context:      deps.Context,
		compressor:   deps.Compressor,
		session:      deps.Session,
		memory:       deps.Memory,
		agents:       deps.Agents,
		subBudget:    deps.SubAgents,
		skills:       deps.Skills,
		config:       cfg,
		state:        &TaskState{TaskID: cfg.SessionID},
		history:      make([]Message, 0),
		guards:       guard,
		progressLoop: NewLoopTracker(4, 6, true), // 4th nudge / 6th block, progress resets
	}

	// Persistent memory (memory_markers, decisions, open_questions,
	// assumptions) is loaded lazily on /resume (see SetHistory), NOT at
	// startup. A fresh session must not inherit the project's cross-task
	// memory — that stale state leaked into Block B every turn and biased the
	// model. Only a manual /resume restores prior-session findings.

	// Initialize eval store
	evalPath := cfg.EvalStoreDir
	if evalPath == "" {
		evalPath = defaultEvalPath()
	} else {
		evalPath = filepath.Join(evalPath, "records.jsonl")
	}
	if store, err := NewJSONLEvalStore(evalPath); err == nil {
		e.evalStore = store
	}

	if deps.AfterEngine != nil {
		deps.AfterEngine(e)
	}

	return e
}

func (e *Engine) SetOnProgress(fn ProgressFunc) {
	e.config.OnProgress = fn
	// Propagate to all sub-agents so their tool execution is visible in the UI
	if e.agents != nil {
		type progressSetter interface{ SetOnProgress(ProgressFunc) }
		e.agents.ForEach(func(a Agent) {
			if ps, ok := a.(progressSetter); ok {
				ps.SetOnProgress(fn)
			}
		})
	}
}

// SetSessionID 切换会话 ID（/resume continue 语义：后续事件写入该文件）。
func (e *Engine) SetSessionID(id string) {
	e.config.SessionID = id
	if e.state != nil {
		e.state.TaskID = id
	}
}

// SetHistory 预载恢复的会话历史（首次 Run() 前调用）。
// 预载的消息不再重复落盘（persistedCount 同步）。
func (e *Engine) SetHistory(h []Message) {
	e.history = append([]Message(nil), h...)
	e.persistedCount = len(e.history)
	// The history is being replaced (/resume continues another session), so
	// every suspended run points at a history that no longer exists — drop
	// them rather than leave a dangling, unanswerable question behind.
	e.dropSuspendedTasks()
	// /resume path: only a manual resume restores the project's persistent
	// memory. Lazy here so a fresh session starts with an empty memory slice
	// (no stale cross-task markers/decisions in Block B). loadPersistentMemory
	// is idempotent, so repeat calls are no-ops.
	e.loadPersistentMemory()
}

func (e *Engine) Run(ctx context.Context, userMsg string) (*EngineResponse, error) {
	if e.state == nil {
		return nil, fmt.Errorf("state not initialized")
	}
	// Persist this Run's conversation history as message events on every exit
	// path (Run has ~20 returns). tool messages are stored as brief digests.
	defer e.persistHistory()
	// Background async sub-agent tasks never outlive this Run.
	defer e.cancelBackgroundTasks()
	// Ensure the background task table exists for this Run (paired with the
	// cancelBackgroundTasks defer on exit).
	e.initBackgroundTasks()
	if err := e.emitEvent("user_message", StageIntake, userMsg); err != nil {
		return nil, err
	}
	e.history = append(e.history, Message{Role: "user", Content: userMsg, Timestamp: time.Now()})

	// Drain steer queue: inject messages retained from a previous Blocked run.
	e.drainSteerQueue()

	// Reset the no-progress breaker and the repeat counters on each Run: a user
	// retrying or revisiting a task legitimately re-reads the same core files,
	// so cross-Run accumulation would misreport normal work as a loop.
	if e.progressLoop != nil {
		e.progressLoop.Reset()
	}
	e.progressKeys = make(map[string]bool)
	e.repeatCounts = make(map[string]int)
	e.runStartAt = time.Now()
	e.runUsageAccum = ModelUsage{}
	// The delegation tree's token aggregate is per-Run, exactly like the
	// background tasks that feed it: a new Run starts with a fresh budget.
	if e.subBudget != nil {
		e.subBudget.StartRun()
	}
	e.runToolCallCount = 0
	e.runErrorCount = 0
	e.stopHookActive = false
	e.stopHookRetryCount = 0
	e.contextRetryDone = false
	// Per Run: the step panel's newest snapshot, and one completion nudge at
	// most — a fresh Run gives the model a fresh chance to finish its panel.
	e.lastTodos = nil
	e.todoNudgeSent = false

	// Skill command handling — /skills (list) and /skill <name> (load)
	if sc := parseSkillCommand(userMsg); sc != nil {
		switch sc.action {
		case "list":
			skills := e.skills.All()
			if len(skills) == 0 {
				return &EngineResponse{Summary: "No skills available.", Stage: StageAct}, nil
			}
			var b strings.Builder
			b.WriteString("## Available Skills\n\n")
			for _, s := range skills {
				b.WriteString(fmt.Sprintf("- **%s**: %s\n", s.Name, s.Description))
			}
			b.WriteString("\nUse `/<name>` to load a specific skill.")
			return &EngineResponse{Summary: b.String(), Stage: StageAct}, nil

		case "load":
			s := e.skills.Get(sc.name)
			if s == nil {
				// Try case-insensitive match
				for _, sk := range e.skills.All() {
					if strings.EqualFold(sk.Name, sc.name) {
						s = sk
						break
					}
				}
			}
			if s == nil {
				return &EngineResponse{Summary: fmt.Sprintf("Skill '%s' not found. Use `/skills` to list available skills.", sc.name), Stage: StageAct}, nil
			}
			// Load semantics: inject the full content once for this turn,
			// not persistently. Consumed at the next turn start (turn.go:80-86).
			e.pendingPinnedMessages = append(e.pendingPinnedMessages,
				fmt.Sprintf("[SKILL — %s]\n\n%s", s.Name, s.Content))

			taskText := extractTaskTextAfterSkillCmd(userMsg, sc.name)
			if taskText == "" {
				return &EngineResponse{Summary: fmt.Sprintf("✓ Skill `%s` loaded. Full methodology injected for this turn.", s.Name), Stage: StageAct}, nil
			}
			if len(e.history) > 0 {
				e.history[len(e.history)-1].Content = taskText
			}
		}
	}

	// /clear command — reset all task state and wait for new input.
	if isClearCommand(userMsg) {
		e.clearSessionState()
		return &EngineResponse{Summary: "✓ State cleared. Please ask a new question.", Stage: StageAct}, nil
	}

	e.updateGoalFromFirstMessage(userMsg)

	// /confirm N — deterministic confirmation channel. Must run before
	// the free-input clearing so the state set here is what the agent
	// sees in this same Run, and so a bare "/confirm N" is never treated
	// as user feedback.
	e.handleConfirmCommand(userMsg)

	// 自由输入路径：用户未通过 /confirm N 响应弹出框（走"输入你的意见"
	// 回输入框，或无 options 的 ask_user 直接输入），当前呈现的这条作废，
	// 避免残留到下一轮再次弹出；队列里其余问题各自仍绑定着自己的 handle
	// （pinned 摘要会继续列出它们），因此只消费队首、不整体清空。
	// 危险命令同理：用户没有用 /confirm 1/2 确认就输入了别的消息，
	// pending 状态作废，防止残留导致后续误确认。
	e.consumeAskUser()
	if e.state.PendingDangerousCmd != "" && !isConfirmCommand(userMsg) {
		e.state.PendingDangerousCmd = ""
	}

	// Record the history boundary for this Run() so buildRunSummary only
	// surfaces assistant text produced THIS run. Without this, a prior run's
	// narration gets returned every turn when the model only emits tool calls.
	e.runStartHistoryLen = len(e.history)

	// Continue from the session-level turn counter instead of resetting to 0.
	// This prevents duplicate turn numbers in AccumulatedBlocks when Run() is
	// called multiple times (e.g., across user messages in the same session).
	turns := e.state.TurnNumber
	maxTurns := e.config.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 50 // safe default to prevent infinite loops
	}
	var completionSummary string
	for {
		select {
		case <-ctx.Done():
			return &EngineResponse{Summary: "Task cancelled.", Stage: StageAct, Blocked: true, BlockedBy: "cancelled", FinishReason: "cancelled"}, nil
		default:
		}
		if turns >= maxTurns {
			msg := "Reached maximum turn limit. The agent was unable to complete the task. Please narrow the scope and retry."
			loopLog.Printf("max turns reached (%d)", maxTurns)
			if err := e.emitEvent("max_turns", StageAct, turns); err != nil {
				loopLog.Printf("emit max_turns event: %v", err)
			}
			return &EngineResponse{Summary: msg, Stage: StageAct, Blocked: true, BlockedBy: "max_turns", FinishReason: "max_turns"}, nil
		}
		e.state.TurnNumber = turns
		turnResult, err := e.executeTurn(ctx)
		if err != nil {
			loopLog.Printf("executeTurn failed (turn=%d): %v", turns, err)
			e.runErrorCount++
			return nil, err
		}
		if turnResult.Blocked {
			e.runErrorCount++
			summary := buildRunSummary(e.history, e.runStartHistoryLen, e.runToolCallCount)
			// The awaiting_user block's Questions already contain the model's
			// question text. Suppress the run summary (which would re-echo
			// the question or a stale narration) so the user sees exactly one
			// copy of the question waiting for their input.
			if turnResult.BlockedBy == "awaiting_user" {
				summary = ""
			}
			// A pending dangerous command presents a deterministic confirmation
			// popup: option 1 = confirm, option 2 = cancel (the last "自由输入"
			// entry lets the user type an alternative command). /confirm 1/2
			// is consumed by handleConfirmCommand on the next Run.
			var opts []string
			if e.state.PendingDangerousCmd != "" {
				opts = []string{"Confirm", "Cancel"}
			}
			return &EngineResponse{
				Summary:      summary,
				Questions:    turnResult.Questions,
				Options:      opts,
				Stage:        StageAct,
				Blocked:      true,
				BlockedBy:    turnResult.BlockedBy,
				FinishReason: turnResult.FinishReason,
			}, nil
		}
		if turnResult.Done {
			completionSummary = turnResult.CompletionSummary
			// If steer messages were queued while the agent was running,
			// inject them and continue the loop instead of returning.
			if e.drainSteerQueue() {
				completionSummary = ""
				continue
			}
			break
		}

		// Progress guard — the only loop guard that can end a Run: N consecutive
		// turns without a progress signal (novel read/search, successful
		// edit/write/revert/bash, handoff, novel lsp query) → nudge then block.
		// It catches the "narration + repeat" loop where every turn is a
		// different call, which per-target counting alone would not see.
		if e.progressLoop != nil {
			action := e.progressLoop.Check("", turnResult.MadeProgress)
			switch action.Type {
			case GuardDiagnose:
				nudge := buildProgressNudge()
				e.pendingPinnedMessages = append(e.pendingPinnedMessages, nudge)
				loopLog.Printf("progress-loop nudge pinned")
			case GuardBlock:
				msg := buildProgressBlockMsg()
				loopLog.Printf("progress-loop block")
				return &EngineResponse{
					Summary:      msg,
					Stage:        StageAct,
					Blocked:      true,
					BlockedBy:    "loop_guard",
					FinishReason: "loop_detected",
				}, nil
			}
		}

		// Drain steer queue before next turn: injects user messages queued
		// during the previous turn's tool execution.
		e.drainSteerQueue()

		turns++
	}
	// Advance session turn counter past the last executed turn so the next
	// Run() call continues from the correct position. +1 because 'turns' was
	// not incremented after a Done break — it still points to the completed turn.
	e.state.TurnNumber = turns + 1

	if err := e.emitEvent("act_complete", StageAct, nil); err != nil {
		return nil, err
	}

	if err := e.verifyAndCompact(ctx); err != nil {
		return nil, err
	}

	// Persist cross-session memory after the run completes so findings
	// (markers, decisions, open questions, assumptions) survive restarts.
	e.persistMemory()

	// Record efficiency eval at end of Run()
	e.recordRunEval()

	summary := completionSummary
	if summary == "" {
		summary = buildRunSummary(e.history, e.runStartHistoryLen, e.runToolCallCount)
	}
	loopLog.Printf("Run done: turns=%d total=%s tool_calls=%d errors=%d usage prompt=%d completion=%d cache_hit=%d cache_miss=%d",
		e.state.TurnNumber, time.Since(e.runStartAt), e.runToolCallCount, e.runErrorCount,
		e.runUsageAccum.PromptTokens, e.runUsageAccum.CompletionTokens,
		e.runUsageAccum.CacheHitTokens, e.runUsageAccum.CacheMissTokens)
	// ask_user with options: the agent asked a question and declared candidate
	// answers — present them as selectable options (raw options + 输入你的意见).
	// 无 options 已由 awaiting_user Blocked 分支（loop.go:823）处理，不走此处。
	if req := e.peekAskUser(); req != nil && len(req.Options) > 0 {
		return &EngineResponse{
			Summary: summary,
			Options: e.askUserOptions(),
			Stage:   StageAct,
		}, nil
	}
	return &EngineResponse{Summary: summary, Stage: StageVerifyCompact}, nil
}

// askUserOptions returns the presentation options after a Run.
// Pending ask_user with options → raw options plus the free-input entry.
// Pending ask_user without options → nil (the question is presented via the
// awaiting_user Blocked path, user answers freely). No pending ask_user →
// nil (no options popup; the model decides whether to ask via ask_user).
func (e *Engine) askUserOptions() []string {
	req := e.peekAskUser()
	if req == nil || len(req.Options) == 0 {
		return nil
	}
	opts := make([]string, 0, len(req.Options)+1)
	opts = append(opts, req.Options...)
	opts = append(opts, "输入你的意见")
	return opts
}

// pushAskUser appends a question to the presentation queue.
func (e *Engine) pushAskUser(req *AskUserRequest) {
	if req == nil {
		return
	}
	e.askUserMu.Lock()
	e.pendingAskUser = append(e.pendingAskUser, req)
	e.askUserMu.Unlock()
}

// peekAskUser returns the question currently presented (the queue head), or nil.
func (e *Engine) peekAskUser() *AskUserRequest {
	e.askUserMu.Lock()
	defer e.askUserMu.Unlock()
	if len(e.pendingAskUser) == 0 {
		return nil
	}
	return e.pendingAskUser[0]
}

// clearAskUser drops every pending question. Used only when the runs behind
// them are gone too (e.g. the conversation history is replaced).
func (e *Engine) clearAskUser() {
	e.askUserMu.Lock()
	e.pendingAskUser = nil
	e.askUserMu.Unlock()
}

// dropSuspendedTasks removes every suspended (awaiting_user) entry — nothing to
// cancel, they hold no work — together with the questions they were waiting on.
// Used when those runs' histories stop being valid (e.g. /resume replaces the
// conversation), so no dangling question survives.
func (e *Engine) dropSuspendedTasks() {
	e.bgMu.Lock()
	n := 0
	for id, t := range e.bgTasks {
		if t.state == bgStateAwaitingUser {
			delete(e.bgTasks, id)
			n++
		}
	}
	e.bgMu.Unlock()
	if n > 0 {
		loopLog.Printf("dropped %d suspended sub-agent job(s): their history was replaced", n)
	}
	e.clearAskUser()
}

// consumeAskUser drops the queue head — the question that was just answered or
// invalidated. The remaining questions keep their own job handles, so they must
// NOT be dropped with it.
func (e *Engine) consumeAskUser() {
	e.askUserMu.Lock()
	if len(e.pendingAskUser) > 0 {
		e.pendingAskUser = e.pendingAskUser[1:]
	}
	e.askUserMu.Unlock()
}

// dropAskUserFor removes the pending questions belonging to the given job
// handles. Called when a suspended job is closed: the entry is gone, so its
// question must not remain presented (the user's answer could never be
// delivered — agent_resume would report "not found").
func (e *Engine) dropAskUserFor(runIDs map[string]bool) {
	if len(runIDs) == 0 {
		return
	}
	e.askUserMu.Lock()
	defer e.askUserMu.Unlock()
	kept := e.pendingAskUser[:0]
	for _, q := range e.pendingAskUser {
		if q != nil && q.RunID != "" && runIDs[q.RunID] {
			continue
		}
		kept = append(kept, q)
	}
	e.pendingAskUser = kept
}

// buildRunSummary produces the user-facing summary for a Run() by walking the
// history backwards. It falls back through three levels so that an agent which
// never produced a visible text reply is never falsely reported as "Done/完成":
//
//  1. Last assistant message with non-empty Content (the normal case).
//  2. Last assistant message with non-empty ReasoningContent (thinking counts
//     as output — better than a fake "Done").
//  3. A diagnostic string naming the tool-call count, so the user can see the
//     agent stalled instead of being told it "completed".
func buildRunSummary(history []Message, startIdx int, toolCallCount int) string {
	summary := ""
	for i := len(history) - 1; i >= startIdx; i-- {
		if history[i].Role == "assistant" && history[i].Content != "" {
			summary = history[i].Content
			break
		}
	}
	if summary == "" {
		for i := len(history) - 1; i >= startIdx; i-- {
			if history[i].Role == "assistant" && history[i].ReasoningContent != "" {
				summary = history[i].ReasoningContent
				break
			}
		}
	}
	summary = stripDSMLTokens(summary)
	if summary != "" {
		return summary
	}
	// No textual output of any kind. Report honestly instead of claiming "Done".
	return fmt.Sprintf("(no text reply generated; %d tool calls executed this run)", toolCallCount)
}

// isConfirmCommand reports whether userMsg is a valid "/confirm N" command.
func isConfirmCommand(userMsg string) bool {
	_, ok := parseConfirmCommand(userMsg)
	return ok
}

func (e *Engine) emitEvent(eventType string, stage Stage, payload any) error {
	if e.session == nil {
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	event := Event{SessionID: e.config.SessionID, WorkDir: e.config.WorkDir, Type: eventType, Stage: stage, Timestamp: time.Now(), Payload: data}
	if err := e.session.AppendEvent(event); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

// persistHistory writes this Run's new conversation messages as message
// events. user/assistant are stored in full; tool messages keep only a
// briefDigest; reasoning_content is not persisted.
//
// The on-disk transcript is a display/audit record and the source for /resume.
// /resume rebuilds a plain-text history (see RebuildHistory): it drops tool
// messages and strips both ToolCalls and ReasoningContent, so the restored
// conversation carries no tool chain and no chain-of-thought. That sidesteps
// the two API contracts that only apply when those fields are present:
// assistant(tool_calls) must be followed by tool responses, and a request with
// tools must echo back every turn's reasoning_content. Storing reasoning here
// would only inflate the session file, since it is discarded on restore.
func (e *Engine) persistHistory() {
	if e.session == nil {
		return
	}
	if e.persistedCount > len(e.history) {
		// Compression replaced history → rewrite from 0 (restores window
		// semantics and avoids an out-of-bounds slice panic).
		e.persistedCount = 0
	}
	for _, msg := range e.history[e.persistedCount:] {
		persisted := msg
		persisted.ReasoningContent = ""
		if persisted.Role == "tool" {
			persisted.Content = briefDigest(persisted.Content)
		}
		if err := e.emitEvent(EventTypeMessage, StageAct, persisted); err != nil {
			loopLog.Printf("persist message event: %v", err)
		}
	}
	e.persistedCount = len(e.history)
}

func (e *Engine) verifyAndCompact(ctx context.Context) error {
	if e.context == nil || e.compressor == nil {
		return nil
	}
	messages := e.context.Build(e.state, e.history, nil)
	tokens := e.context.EstimateTokens(messages)
	layer, should := e.compressor.ShouldCompress(tokens, e.config.MaxContextTokens)
	if should {
		compacted, err := e.compressor.Compress(ctx, layer, e.state, e.history)
		if err != nil {
			// Compression is best-effort — same as executeTurn (turn.go:53-56).
			// A failure (e.g. flash model timeout) must NOT suppress the run
			// summary, which is built next. The UI silently suppresses errors
			// containing "context deadline exceeded" (model.go:1544-1547), so
			// returning here would cause the user to see no output at all.
			loopLog.Printf("compress failed (non-fatal, skipping): %v", err)
		} else {
			e.history = compacted
		}
	}
	if err := e.emitEvent("verify_compact", StageVerifyCompact, nil); err != nil {
		return err
	}
	return nil
}

// recordRunEval writes an efficiency EvalRecord for the current Run().
// Phase is "main_agent" to distinguish from conference-phase scorecard records.
func (e *Engine) recordRunEval() {
	if e.evalStore == nil {
		return
	}
	usage := e.runUsageAccum
	if usage.TotalTokens == 0 {
		return // nothing to record
	}

	goal := e.state.Goal
	if len(goal) > 100 {
		goal = goal[:100]
	}

	// Estimate cost from pricing config
	var cost float64
	if p, ok := e.config.Pricing.Models[e.config.ModelName]; ok {
		cost = float64(usage.PromptTokens)*p.InputPricePerToken +
			float64(usage.CompletionTokens)*p.OutputPricePerToken
		cost -= float64(usage.CacheHitTokens) * (p.InputPricePerToken - p.CacheHitInputPricePerToken)
	} else {
		def := e.config.Pricing.Default
		cost = float64(usage.PromptTokens)*def.InputPricePerToken +
			float64(usage.CompletionTokens)*def.OutputPricePerToken
	}
	if cost < 0 {
		cost = 0
	}

	rec := EvalRecord{
		Timestamp:         time.Now(),
		SessionID:         e.config.SessionID,
		PromptVersion:     e.config.PromptVersion,
		Phase:             "main_agent",
		IterationCount:    e.state.TurnNumber,
		GoalSnippet:       goal,
		PromptTokens:      usage.PromptTokens,
		CompletionTokens:  usage.CompletionTokens,
		CacheHitTokens:    usage.CacheHitTokens,
		CacheMissTokens:   usage.CacheMissTokens,
		DurationMs:        time.Since(e.runStartAt).Milliseconds(),
		ToolCallCount:     e.runToolCallCount,
		ModifiedFileCount: len(e.state.ModifiedFiles),
		ErrorCount:        e.runErrorCount,
		CostEstimate:      cost,
	}
	if err := e.evalStore.Insert(rec); err != nil {
		loopLog.Printf("record eval: %v", err)
	}
}

// accumulateUsage adds a sub-agent's token usage to the main engine's
// per-Run accumulator. Thread-safe: uses usageMu for concurrent goroutine access.
func (e *Engine) accumulateUsage(usage *ModelUsage) {
	if usage == nil {
		return
	}
	e.usageMu.Lock()
	e.runUsageAccum.PromptTokens += usage.PromptTokens
	e.runUsageAccum.CompletionTokens += usage.CompletionTokens
	e.runUsageAccum.TotalTokens += usage.TotalTokens
	e.runUsageAccum.CacheHitTokens += usage.CacheHitTokens
	e.runUsageAccum.CacheMissTokens += usage.CacheMissTokens
	e.usageMu.Unlock()
}

// msgIsChinese is gone: the engine no longer selects prompts by session language.

// skillCommand represents a parsed /skill or /skills command.
type skillCommand struct {
	action string // "list" or "load"
	name   string // skill name for "load"
}

// parseSkillCommand checks if userMsg is a /skill, /skills, or /<skillname> command.
func parseSkillCommand(userMsg string) *skillCommand {
	trimmed := strings.TrimSpace(userMsg)
	if !strings.HasPrefix(trimmed, "/") {
		return nil
	}
	if idx := strings.IndexByte(trimmed, '\n'); idx > 0 {
		trimmed = trimmed[:idx]
	}
	rest := trimmed[1:]
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.ToLower(parts[0])

	// Reserved commands that are handled elsewhere.
	if cmd == "clear" || cmd == "confirm" {
		return nil
	}

	switch cmd {
	case "skills":
		return &skillCommand{action: "list"}
	case "skill":
		if len(parts) < 2 {
			return &skillCommand{action: "list"}
		}
		return &skillCommand{action: "load", name: strings.ToLower(parts[1])}
	default:
		if isValidSkillName(cmd) {
			return &skillCommand{action: "load", name: cmd}
		}
		return nil
	}
}

func isValidSkillName(name string) bool {
	if len(name) == 0 || len(name) > 30 {
		return false
	}
	for _, r := range name {
		if r != '-' && r != '_' && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func extractTaskTextAfterSkillCmd(userMsg string, skillName string) string {
	trimmed := strings.TrimSpace(userMsg)
	prefix := "/" + skillName
	if !strings.HasPrefix(strings.ToLower(trimmed), prefix) {
		prefix = "/skill " + skillName
		if !strings.HasPrefix(strings.ToLower(trimmed), prefix) {
			return trimmed
		}
	}
	rest := strings.TrimSpace(trimmed[len(prefix):])
	return rest
}

// isClearCommand detects the /clear signal that resets all session state.
func isClearCommand(userMsg string) bool {
	trimmed := strings.TrimSpace(userMsg)
	return trimmed == "/clear" || strings.HasPrefix(trimmed, "/clear ")
}

// parseConfirmCommand extracts the option number from a "/confirm N" command.
// N is 1-based. Returns (0, false) for anything that is not a valid "/confirm N".
func parseConfirmCommand(userMsg string) (int, bool) {
	trimmed := strings.TrimSpace(userMsg)
	if !strings.HasPrefix(trimmed, "/confirm ") {
		return 0, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "/confirm "))
	n, err := strconv.Atoi(rest)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// handleConfirmCommand processes a /confirm N message deterministically,
// bypassing any text-based confirmation heuristics.
//
// Priority 1: a pending dangerous command (PendingDangerousCmd). /confirm 1
// confirms it (marks the scope guard and injects the re-issue hint), /confirm 2
// cancels it; any other N cancels as well (only two options are presented).
// Priority 2: ask_user options (pendingAskUser with a non-empty Options list)
// — /confirm N selects the Nth option and the choice is injected into history.
// With neither pending, /confirm N is a silent no-op (consumed but produces
// no history rewrite). The last popup item ("输入你的意见") never reaches
// here — the UI returns to the input box. Returns true if userMsg was a valid
// /confirm command.
func (e *Engine) handleConfirmCommand(userMsg string) bool {
	n, ok := parseConfirmCommand(userMsg)
	if !ok {
		return false
	}

	// Priority 1: dangerous command confirmation.
	if e.state.PendingDangerousCmd != "" {
		confirmedCmd := e.state.PendingDangerousCmd
		if n == 1 {
			// /confirm 1 → confirm. Mark the scope guard so the re-issued
			// command passes, then inject the user confirmation + re-issue
			// hint so the agent knows to retry the previously blocked command.
			e.guards.scope.ConfirmDangerous(confirmedCmd)
			e.history = append(e.history, Message{Role: "user", Content: "✓ Dangerous command confirmed, proceeding...", Timestamp: time.Now()})
			reissueHint := fmt.Sprintf("The user confirmed the dangerous command. Please re-issue the previously blocked command: `%s`", confirmedCmd)
			e.history = append(e.history, Message{Role: "user", Content: reissueHint, Timestamp: time.Now()})
		}
		// /confirm 2 (or any other N) cancels: consume the presented question,
		// no Confirm.
		e.state.PendingDangerousCmd = ""
		e.consumeAskUser()
		loopLog.Printf("handleConfirmCommand: dangerous command confirmed=%v (n=%d)", n == 1, n)
		return true
	}

	if len(e.history) > 0 && e.history[len(e.history)-1].Role == "user" {
		head := e.peekAskUser()
		switch {
		case head != nil && len(head.Options) > 0 && n >= 1 && n <= len(head.Options):
			label := head.Options[n-1]
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"The user selected: %s. Proceed with that option.", label)
		case head != nil && len(head.Options) > 0:
			// 有声明方案但编号越界（n < 1 或 n > len(options)）：明确告知
			// agent 用户选择无效，由其决定下一步。
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"The user selected an out-of-range option number %d; ask the user again.", n)
			loopLog.Printf("handleConfirmCommand: /confirm %d out of range (pending options=%d)", n, len(head.Options))
		}
	}
	// 队首问题已被消费（用户已选择、越界或确认），其余问题保留。
	e.consumeAskUser()
	loopLog.Printf("handleConfirmCommand: /confirm %d processed", n)
	return true
}

// loadPersistentMemory loads the cross-session memory snapshot for this
// project (if any) and merges it into TaskState. Called lazily on /resume
// (SetHistory), not at startup, so a fresh session never inherits the
// project's cross-task memory. Idempotent: after the first load, subsequent
// calls are no-ops. Failures are non-fatal: a corrupt/missing memory file
// must not prevent the agent from starting.
func (e *Engine) loadPersistentMemory() {
	if e.memory == nil || e.memoryLoaded {
		return
	}
	e.memoryLoaded = true
	snap, err := e.memory.Load()
	if err != nil {
		loopLog.Printf("load persistent memory: %v", err)
		return
	}
	if snap == nil {
		return
	}
	if len(snap.MemoryMarkers) > 0 {
		e.state.MemoryMarkers = appendUniqMarkers(e.state.MemoryMarkers, snap.MemoryMarkers...)
	}
	for _, d := range snap.Decisions {
		if d.Text == "" {
			continue
		}
		if !containsDecisionText(e.state.Decisions, d.Text) {
			e.state.Decisions = append(e.state.Decisions, d)
		}
	}
	for _, q := range snap.OpenQuestions {
		if !containsString(e.state.OpenQuestions, q) {
			e.state.OpenQuestions = append(e.state.OpenQuestions, q)
		}
	}
	for _, a := range snap.Assumptions {
		if !containsString(e.state.Assumptions, a) {
			e.state.Assumptions = append(e.state.Assumptions, a)
		}
	}
	if len(snap.MemoryMarkers) > 0 || len(snap.Decisions) > 0 || len(snap.OpenQuestions) > 0 || len(snap.Assumptions) > 0 {
		loopLog.Printf("loaded persistent memory: %d markers, %d decisions, %d open questions, %d assumptions",
			len(snap.MemoryMarkers), len(snap.Decisions), len(snap.OpenQuestions), len(snap.Assumptions))
	}
}

// persistMemory snapshots the persistent subset of TaskState to the memory
// store. Called at the end of each Run() so findings survive process restarts.
func (e *Engine) persistMemory() {
	if e.memory == nil {
		return
	}
	snap := &MemorySnapshot{
		CWD:           e.config.WorkDir,
		UpdatedAt:     time.Now(),
		MemoryMarkers: e.state.MemoryMarkers,
		Decisions:     e.state.Decisions,
		OpenQuestions: e.state.OpenQuestions,
		Assumptions:   e.state.Assumptions,
	}
	if err := e.memory.Save(snap); err != nil {
		loopLog.Printf("persist memory: %v", err)
	}
}

// clearSessionState resets all task-level state to a fresh session.
// Conversation history is preserved for project context.
func (e *Engine) clearSessionState() {
	e.state.Goal = ""
	e.state.PlanConfirmed = false
	e.state.MemoryMarkers = nil
	e.state.Decisions = nil
	e.state.Plan = nil
	e.state.WorkingSet = WorkingSet{}
	e.state.OpenQuestions = nil
	e.state.ModifiedFiles = nil
	e.state.Constraints = nil
	e.state.Assumptions = nil
	e.state.FileCollapse = nil
	e.state.CallChain = nil
	e.state.EditScopeFiles = 0
	e.state.PendingDangerousCmd = ""
	e.state.TurnNumber = 0
	e.state.ConsecutiveFailures = 0

	e.steerMu.Lock()
	e.steerQueue = nil
	e.steerMu.Unlock()

	// /clear also wipes the cross-session persistent memory for this project,
	// so a cleared state does not resurface on the next process start.
	if e.memory != nil {
		if err := e.memory.Clear(); err != nil {
			loopLog.Printf("clear persistent memory: %v", err)
		}
	}
}

// Steer queues a user message to be injected into the conversation at the
// next turn boundary (after current tool execution completes, before the
// next LLM call). Called by the UI when the user submits input during an
// active run.
func (e *Engine) Steer(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	e.steerMu.Lock()
	defer e.steerMu.Unlock()
	e.steerQueue = append(e.steerQueue, msg)
	if e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{Type: "steer_queued", Detail: msg})
	}
}

// steerQueueLen returns the number of messages currently queued for
// injection. Used by the soft-interrupt check to detect a steer message
// arriving mid-stream (see turn.go checkSteerMidStream).
func (e *Engine) steerQueueLen() int {
	e.steerMu.Lock()
	defer e.steerMu.Unlock()
	return len(e.steerQueue)
}

// drainSteerQueue appends all queued steer messages to history as user
// messages. Returns true if any messages were injected.
func (e *Engine) drainSteerQueue() bool {
	e.steerMu.Lock()
	pending := e.steerQueue
	e.steerQueue = nil
	e.steerMu.Unlock()

	if len(pending) == 0 {
		return false
	}
	for _, msg := range pending {
		e.history = append(e.history, Message{
			Role:      "user",
			Content:   msg,
			Timestamp: time.Now(),
		})
	}
	if e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{
			Type:   "steer_injected",
			Detail: strings.Join(pending, "\n"),
		})
	}
	loopLog.Printf("steer: injected %d message(s)", len(pending))
	return true
}

// buildProgressNudge builds the nudge message for the 4th consecutive
// no-progress turn.
func buildProgressNudge() string {
	return "[PROGRESS NUDGE] You have spent several turns on read-only/planning work without any code change or conclusion. " +
		"Take the next concrete action (write a test, modify code, or produce a conclusion) instead of re-stating state or re-reading."
}

// buildProgressBlockMsg builds the block message for the 6th consecutive
// no-progress turn.
func buildProgressBlockMsg() string {
	return "Detected a no-progress loop: you have spent many turns on read-only/planning work without a code change or conclusion, despite a nudge. " +
		"The agent may be stuck. Please clarify: conclude from existing content, or adjust the task direction?"
}
