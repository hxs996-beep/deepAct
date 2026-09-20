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
	"unicode"

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

type EngineDeps struct {
	Model       ModelClient
	Tools       ToolExecutor
	Context     ContextBuilder
	Compressor  Compressor
	Session     SessionStore
	Memory      MemoryStore
	Agents      *AgentRegistry
	Skills      *skill.Registry
	Router      ModelRouter
	MCPManagers []io.Closer // MCP server connections to close on shutdown

	// AfterEngine, when set, is called at the end of NewEngine with the new
	// Engine instance. Used by cmd/run.go to register tools that need a live
	// Engine reference (e.g. the SubAgentTool backends).
	AfterEngine func(*Engine)
}

type Engine struct {
	model        ModelClient
	tools        ToolExecutor
	context      ContextBuilder
	compressor   Compressor
	session      SessionStore
	memory       MemoryStore
	agents       *AgentRegistry
	skills       *skill.Registry
	router       ModelRouter
	config       EngineConfig
	state        *TaskState
	history      []Message
	steerMu      sync.Mutex
	steerQueue   []string
	guards       *GuardSystem
	readLoop     *LoopTracker
	errorLoop    *LoopTracker
	progressLoop *LoopTracker
	// progressKeys tracks "new information" keys already seen this Run
	// ("read:path::scope", "grep:<pattern>:<path>", "glob:<pattern>:<path>").
	// A novel key counts as progress for the progress LoopTracker
	// (progressLoop), so legitimate investigation — reading or searching new
	// content — is not mistaken for a no-progress loop. Reset each Run.
	progressKeys map[string]bool
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
	pendingAskUser *AskUserRequest
	// askUserMu guards pendingAskUser: parallel sub-agent handoffs can
	// bubble up questions concurrently, so the write must be serialized.
	askUserMu sync.Mutex

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

	// isChinese is set once from the first user message in the session.
	// All per-turn UI messages (skill list, activation prompts, etc.) use
	// this instead of recomputing msgIsChinese per-turn, which would switch
	// to English when the user types "ok"/"yes" to confirm.
	isChinese    bool
	langDetected bool

	// memoryLoaded guards lazy persistent-memory loading. Memory is restored
	// only on /resume (SetHistory), never at startup, so a fresh session
	// starts clean of cross-task markers/decisions.
	memoryLoaded bool
}

func NewEngine(cfg EngineConfig, deps EngineDeps) *Engine {
	guard := &GuardSystem{
		scope: NewScopeGuard(cfg.AutoConfirmScope),
		loop:  NewLoopTracker(0, 6, false), // block after 6 repeats of same (tool, path)
	}
	e := &Engine{
		model:        deps.Model,
		tools:        deps.Tools,
		context:      deps.Context,
		compressor:   deps.Compressor,
		session:      deps.Session,
		memory:       deps.Memory,
		agents:       deps.Agents,
		skills:       deps.Skills,
		router:       deps.Router,
		config:       cfg,
		state:        &TaskState{TaskID: cfg.SessionID},
		history:      make([]Message, 0),
		guards:       guard,
		readLoop:     NewLoopTracker(3, 4, false), // 3rd nudge / 4th block
		errorLoop:    NewLoopTracker(0, 3, true),  // 3 errors → block, success resets
		progressLoop: NewLoopTracker(4, 6, true),  // 4th nudge / 6th block, progress resets
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
	// Detect language once at session start, not per-turn.
	// This prevents "ok"/"yes"/"confirm" from switching UI to English.
	if !e.langDetected {
		e.isChinese = msgIsChinese(userMsg)
		e.langDetected = true
		// Broadcast the session-locked language to the shared compressor and
		// guard instances so their LLM prompts / messages pick the right variant.
		userLang := ""
		if e.isChinese {
			userLang = "中文"
		}
		if e.compressor != nil {
			e.compressor.SetUserLang(userLang)
		}
		if e.guards != nil {
			e.guards.SetLanguage(e.isChinese)
		}
	}
	zh := e.isChinese
	if err := e.emitEvent("user_message", StageIntake, userMsg); err != nil {
		return nil, err
	}
	e.history = append(e.history, Message{Role: "user", Content: userMsg, Timestamp: time.Now()})

	// Drain steer queue: inject messages retained from a previous Blocked run.
	e.drainSteerQueue()

	// Reset loop tracking (loop guard + read loop) on each Run. Read
	// counts must NOT accumulate across Runs: a user retrying or revisiting a
	// task legitimately re-reads the same core files, and cross-Run accumulation
	// falsely blocked normal reads as "loops" (maxRepeats reached across
	// retries). Within a Run, the readLoop tracker still catches true read
	// loops (4th same-read blocks). Edit/write loop counts also reset per Run -
	// same-Run repetition is still caught, and the edit-plan guard +
	// contentHash differentiation cover cross-Run edit cases. The errorLoop
	// tracker persists (error streaks across Runs are meaningful).
	if e.guards != nil && e.guards.loop != nil {
		e.guards.loop.Reset()
	}
	if e.readLoop != nil {
		e.readLoop.Reset()
	}
	if e.progressLoop != nil {
		e.progressLoop.Reset()
	}
	e.progressKeys = make(map[string]bool)
	e.runStartAt = time.Now()
	e.runUsageAccum = ModelUsage{}
	e.runToolCallCount = 0
	e.runErrorCount = 0
	e.stopHookActive = false
	e.stopHookRetryCount = 0

	// Skill command handling — /skills (list) and /skill <name> (load)
	if sc := parseSkillCommand(userMsg); sc != nil {
		switch sc.action {
		case "list":
			skills := e.skills.All()
			if len(skills) == 0 {
				msg := "No skills available."
				if zh {
					msg = "当前没有可用技能。"
				}
				return &EngineResponse{Summary: msg, Stage: StageAct}, nil
			}
			var b strings.Builder
			if zh {
				b.WriteString("## 可用的 Skills\n\n")
			} else {
				b.WriteString("## Available Skills\n\n")
			}
			for _, s := range skills {
				b.WriteString(fmt.Sprintf("- **%s**: %s\n", s.Name, s.Description))
			}
			if zh {
				b.WriteString("\n使用 `/<名称>` 加载指定技能。")
			} else {
				b.WriteString("\nUse `/<name>` to load a specific skill.")
			}
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
				msg := fmt.Sprintf("Skill '%s' not found. Use `/skills` to list available skills.", sc.name)
				if zh {
					msg = fmt.Sprintf("技能 '%s' 不存在。使用 `/skills` 查看可用技能。", sc.name)
				}
				return &EngineResponse{Summary: msg, Stage: StageAct}, nil
			}
			// Load semantics: inject the full content once for this turn,
			// not persistently. Consumed at the next turn start (turn.go:80-86).
			e.pendingPinnedMessages = append(e.pendingPinnedMessages,
				fmt.Sprintf("[SKILL — %s]\n\n%s", s.Name, s.Content))

			taskText := extractTaskTextAfterSkillCmd(userMsg, sc.name)
			if taskText == "" {
				msg := fmt.Sprintf("✓ Skill `%s` loaded. Full methodology injected for this turn.", s.Name)
				if zh {
					msg = fmt.Sprintf("✓ 已加载 skill `%s`：方法论已注入当前回合。", s.Name)
				}
				return &EngineResponse{Summary: msg, Stage: StageAct}, nil
			}
			if len(e.history) > 0 {
				e.history[len(e.history)-1].Content = taskText
			}
		}
	}

	// /clear command — reset all task state and wait for new input.
	if isClearCommand(userMsg) {
		e.clearSessionState()
		msg := "✓ 状态已清理。请提出新的问题。"
		if !zh {
			msg = "✓ State cleared. Please ask a new question."
		}
		return &EngineResponse{Summary: msg, Stage: StageAct}, nil
	}

	e.updateGoalFromFirstMessage(userMsg)

	// /confirm N — deterministic confirmation channel. Must run before
	// the free-input clearing so the state set here is what the agent
	// sees in this same Run, and so a bare "/confirm N" is never treated
	// as user feedback.
	e.handleConfirmCommand(userMsg)

	// 自由输入路径：用户未通过 /confirm N 响应弹出框（走"输入你的意见"
	// 回输入框，或无 options 的 ask_user 直接输入），本组待决问题作废，
	// 避免残留到下一轮再次弹出。危险命令同理：用户没有用 /confirm 1/2
	// 确认就输入了别的消息，pending 状态作废，防止残留导致后续误确认。
	if e.pendingAskUser != nil {
		e.pendingAskUser = nil
	}
	if e.state.PendingDangerousCmd != "" && !isConfirmCommand(userMsg) {
		e.state.PendingDangerousCmd = ""
	}

	// Scope is implicitly confirmed when user sends any message
	if !e.state.ConfirmedScope {
		e.state.ConfirmedScope = true
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
	var lastOp string // "toolName:path" of the previous turn
	consecutiveSameOp := 0
	var completionSummary string
	for {
		select {
		case <-ctx.Done():
			msg := "任务已取消。"
			if !zh {
				msg = "Task cancelled."
			}
			return &EngineResponse{Summary: msg, Stage: StageAct, Blocked: true, BlockedBy: "cancelled", FinishReason: "cancelled"}, nil
		default:
		}
		if turns >= maxTurns {
			msg := "已达到最大轮次限制，Agent 未能完成任务。请缩小范围后重试。"
			if !zh {
				msg = "Reached maximum turn limit. The agent was unable to complete the task. Please narrow the scope and retry."
			}
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
			summary := buildRunSummary(e.history, e.runStartHistoryLen, e.runToolCallCount, zh)
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
				opts = []string{"确认执行", "取消"}
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

		// Loop detection: read ops go through readLoop (two-tier:
		// 3rd same (path,scope) → nudge, 4th → block). Non-read ops keep the
		// original consecutiveSameOp guard (5 consecutive same first-calls →
		// block), which covers tools the loop guard doesn't track (grep/bash/etc.).
		if turnResult.LastOp != "" {
			if strings.HasPrefix(turnResult.LastOp, "read:") {
				action := e.readLoop.Check(turnResult.LastOp, false)
				switch action.Type {
				case GuardDiagnose:
					nudge := buildReadLoopNudge(turnResult.LastOp, zh)
					// Pinned (not persisted to history): the nudge is a runtime
					// reminder to the model for the next turn, not a real user
					// message — avoids context pollution.
					e.pendingPinnedMessages = append(e.pendingPinnedMessages, nudge)
					loopLog.Printf("read-loop nudge pinned for %s", turnResult.LastOp)
				case GuardBlock:
					msg := buildReadLoopBlockMsg(turnResult.LastOp, zh)
					return &EngineResponse{
						Summary:      msg,
						Stage:        StageAct,
						Blocked:      true,
						BlockedBy:    "loop_guard",
						FinishReason: "loop_detected",
					}, nil
				}
				// read ops do not feed consecutiveSameOp
			} else {
				// Error-streak guard: keys on coarse (tool, path) — without the
				// content signature — so repeated FAILING calls with slightly
				// varied args on the same target still accumulate and trip,
				// unlike the content-hash-based loop guard/consecutiveSameOp.
				if e.errorLoop != nil {
					// LoopTracker.Check 取 success 语义；errorLoop 的
					// resetOnSuccess=true，故 LastOpError 需取反（成功=清除连错计数）。
					action := e.errorLoop.Check(coarseOp(turnResult.LastOp), !turnResult.LastOpError)
					if action.Type == GuardBlock {
						msg := "检测到重复的工具错误，Agent 在同一操作上反复失败。请提供新的方向或修正参数。"
						if !zh {
							msg = "Detected repeated tool errors on the same operation. The agent may be stuck. Please provide new direction or correct the parameters."
						}
						loopLog.Printf("error-loop block for %s", turnResult.LastOp)
						return &EngineResponse{Summary: msg, Stage: StageAct, Blocked: true, BlockedBy: "loop_guard", FinishReason: "loop_detected"}, nil
					}
				}
				if turnResult.LastOp == lastOp {
					consecutiveSameOp++
					if consecutiveSameOp >= 5 {
						msg := "检测到重复操作循环，Agent 可能卡住了。请提供新的方向。"
						if !zh {
							msg = "Detected repeated operation loop. The agent may be stuck. Please provide new direction."
						}
						return &EngineResponse{Summary: msg, Stage: StageAct, Blocked: true, BlockedBy: "loop_guard", FinishReason: "loop_detected"}, nil
					}
				} else {
					consecutiveSameOp = 0
				}
				lastOp = turnResult.LastOp
			}
		}

		// Progress guard: N consecutive turns without a progress signal
		// (successful edit/write/revert/bash or handoff) → nudge then block.
		// Catches the "narration + read + todo_write" loop that bypasses all
		// operation-repeat guards: every turn is a *different* operation, so
		// readLoop / consecutiveSameOp / errorLoop never fire.
		if e.progressLoop != nil {
			action := e.progressLoop.Check("", turnResult.MadeProgress)
			switch action.Type {
			case GuardDiagnose:
				nudge := buildProgressNudge(zh)
				e.pendingPinnedMessages = append(e.pendingPinnedMessages, nudge)
				loopLog.Printf("progress-loop nudge pinned")
			case GuardBlock:
				msg := buildProgressBlockMsg(zh)
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
	e.recordRunEval(zh)

	summary := completionSummary
	if summary == "" {
		summary = buildRunSummary(e.history, e.runStartHistoryLen, e.runToolCallCount, zh)
	}
	loopLog.Printf("Run done: turns=%d total=%s tool_calls=%d errors=%d usage prompt=%d completion=%d cache_hit=%d cache_miss=%d",
		e.state.TurnNumber, time.Since(e.runStartAt), e.runToolCallCount, e.runErrorCount,
		e.runUsageAccum.PromptTokens, e.runUsageAccum.CompletionTokens,
		e.runUsageAccum.CacheHitTokens, e.runUsageAccum.CacheMissTokens)
	// ask_user with options: the agent asked a question and declared candidate
	// answers — present them as selectable options (raw options + 输入你的意见).
	// 无 options 已由 awaiting_user Blocked 分支（loop.go:823）处理，不走此处。
	if e.pendingAskUser != nil && len(e.pendingAskUser.Options) > 0 {
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
	if e.pendingAskUser == nil {
		return nil
	}
	if len(e.pendingAskUser.Options) == 0 {
		return nil
	}
	opts := make([]string, 0, len(e.pendingAskUser.Options)+1)
	opts = append(opts, e.pendingAskUser.Options...)
	opts = append(opts, "输入你的意见")
	return opts
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
func buildRunSummary(history []Message, startIdx int, toolCallCount int, zh bool) string {
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
	if summary != "" && !isSubstantiveSummary(summary) {
		summary = ""
	}
	if summary != "" {
		return summary
	}
	// No textual output of any kind. Report honestly instead of claiming "Done".
	if zh {
		return fmt.Sprintf("（本轮未生成回复文本，已执行 %d 次工具调用）", toolCallCount)
	}
	return fmt.Sprintf("(no text reply generated; %d tool calls executed this run)", toolCallCount)
}

// isSubstantiveSummary checks whether a summary string contains meaningful
// analysis content, as opposed to a bare "Done"/"完成" or an echo of the
// internal read_history block. Returns false for empty-shell summaries that
// should be replaced by the diagnostic fallback.
func isSubstantiveSummary(summary string) bool {
	if summary == "" {
		return true // empty is not "unsubstantive" — caller decides fallback
	}

	trimmed := strings.TrimSpace(summary)

	// Rule 1: length threshold.
	// English text under 20 chars with no CJK → too short to be meaningful.
	// Chinese text under 10 chars → too short.
	hasCJK := false
	for _, r := range trimmed {
		if unicode.Is(unicode.Han, r) {
			hasCJK = true
			break
		}
	}
	if hasCJK {
		if len([]rune(trimmed)) < 6 {
			return false
		}
	} else {
		if len(trimmed) < 20 {
			return false
		}
	}

	// Rule 2: bare shell words — exact or nearly exact match.
	shellWords := []string{"done", "完成", "ok", "好的", "i'm done", "im done", "done."}
	lower := strings.ToLower(trimmed)
	for _, w := range shellWords {
		if lower == w {
			return false
		}
	}

	// Rule 3: file-list echo detection.
	// If ≥50% of non-empty lines start with a path-like pattern, treat as echo.
	lines := strings.Split(trimmed, "\n")
	pathLike := 0
	total := 0
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		total++
		// Match lines starting with common path/icon patterns:
		//   - /path/to/file
		//   [<>] path
		//   [@] path
		//   [?] path
		//   [~] path
		//   [>_] path
		if strings.HasPrefix(t, "- /") || strings.HasPrefix(t, "[<>]") ||
			strings.HasPrefix(t, "[@]") || strings.HasPrefix(t, "[?]") ||
			strings.HasPrefix(t, "[~]") || strings.HasPrefix(t, "[>_]") {
			pathLike++
		}
	}
	if total > 0 && pathLike*2 >= total {
		return false
	}

	return true
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
func (e *Engine) recordRunEval(_ bool) {
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

func msgIsChinese(msg string) bool {
	for _, r := range msg {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

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
			msg := "✓ Dangerous command confirmed, proceeding..."
			if e.isChinese {
				msg = "✓ 危险命令已确认，继续执行..."
			}
			e.history = append(e.history, Message{Role: "user", Content: msg, Timestamp: time.Now()})
			reissueHint := fmt.Sprintf("The user confirmed the dangerous command. Please re-issue the previously blocked command: `%s`", confirmedCmd)
			if e.isChinese {
				reissueHint = fmt.Sprintf("用户已确认执行危险命令。请重新执行之前被阻断的命令: `%s`", confirmedCmd)
			}
			e.history = append(e.history, Message{Role: "user", Content: reissueHint, Timestamp: time.Now()})
		}
		// /confirm 2 (or any other N) cancels: clear pending, no Confirm.
		e.state.PendingDangerousCmd = ""
		e.pendingAskUser = nil
		loopLog.Printf("handleConfirmCommand: dangerous command confirmed=%v (n=%d)", n == 1, n)
		return true
	}

	if len(e.history) > 0 && e.history[len(e.history)-1].Role == "user" {
		switch {
		case e.pendingAskUser != nil && len(e.pendingAskUser.Options) > 0 && n >= 1 && n <= len(e.pendingAskUser.Options):
			label := e.pendingAskUser.Options[n-1]
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"用户选择了：%s，请按该方案执行修改。", label)
		case e.pendingAskUser != nil && len(e.pendingAskUser.Options) > 0:
			// 有声明方案但编号越界（n < 1 或 n > len(options)）：明确告知
			// agent 用户选择无效，由其决定下一步。
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"用户选择了无效的方案编号 %d，请重新选择。", n)
			loopLog.Printf("handleConfirmCommand: /confirm %d out of range (pending options=%d)", n, len(e.pendingAskUser.Options))
		}
	}
	// 本组问题已消费（用户已选择、越界或确认），清除避免残留到无关 Run。
	e.pendingAskUser = nil
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
	e.state.ConfirmedScope = false

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

// buildReadLoopNudge builds the nudge message for the 3rd repeated read of the
// same (path, scope). key has form "read:path::scope".
func buildReadLoopNudge(key string, zh bool) string {
	path, scope := splitReadKey(key)
	scopeDesc := describeScope(scope, zh)
	if zh {
		return fmt.Sprintf("[LOOP NUDGE] 你已 3 次读取 %s 的 %s，内容已在对话历史中。"+
			"不要再读取它。请直接基于已有内容产出分析结论；如需新的具体信息，改用 lsp"+
			"（hover/goToDefinition/workspaceSymbol）或读取该文件尚未读过的区段。", path, scopeDesc)
	}
	return fmt.Sprintf("[LOOP NUDGE] You have read %s (%s) 3 times; its content is already in conversation history. "+
		"Do not read it again. Produce your analysis from existing content; for new specifics use lsp "+
		"(hover/goToDefinition/workspaceSymbol) or read an un-read section of the file.", path, scopeDesc)
}

// buildReadLoopBlockMsg builds the block message for the 4th repeated read.
func buildReadLoopBlockMsg(key string, zh bool) string {
	path, scope := splitReadKey(key)
	scopeDesc := describeScope(scope, zh)
	if zh {
		return fmt.Sprintf("检测到重复读取循环：已反复读取 %s（%s），nudge 后仍未改善。"+
			"Agent 可能卡住了。请澄清：是想查看哪段未读内容，还是基于已有内容直接给出结论？", path, scopeDesc)
	}
	return fmt.Sprintf("Repeated read loop detected: %s (%s) has been read repeatedly despite a nudge. "+
		"The agent may be stuck. Please clarify: do you want to view an un-read section, or conclude from existing content?", path, scopeDesc)
}

// buildProgressNudge builds the nudge message for the 4th consecutive
// no-progress turn.
func buildProgressNudge(zh bool) string {
	if zh {
		return "[进度提示] 你已连续多轮只读/规划（读取、更新 todo 等），未产生任何代码修改或结论。" +
			"请直接执行下一步（写测试、改代码或输出结论），不要继续复述当前状态或重复读取。"
	}
	return "[PROGRESS NUDGE] You have spent several turns on read-only/planning work without any code change or conclusion. " +
		"Take the next concrete action (write a test, modify code, or produce a conclusion) instead of re-stating state or re-reading."
}

// buildProgressBlockMsg builds the block message for the 6th consecutive
// no-progress turn.
func buildProgressBlockMsg(zh bool) string {
	if zh {
		return "检测到无进展循环：你已连续多轮只读/规划，未产生任何代码修改或结论，nudge 后仍未改善。" +
			"Agent 可能卡住了。请澄清：是想让我基于已有内容直接给出结论，还是调整任务方向？"
	}
	return "Detected a no-progress loop: you have spent many turns on read-only/planning work without a code change or conclusion, despite a nudge. " +
		"The agent may be stuck. Please clarify: conclude from existing content, or adjust the task direction?"
}

// splitReadKey splits "read:path::scope" into (path, scope).
func splitReadKey(key string) (path, scope string) {
	const prefix = "read:"
	if !strings.HasPrefix(key, prefix) {
		return key, ""
	}
	rest := key[len(prefix):]
	if before, after, ok := strings.Cut(rest, "::"); ok {
		return before, after
	}
	return rest, ""
}

// coarseOp reduces a LastOp key to its "tool:path" form by dropping the
// content-signature suffix ("#sig") used for edit/write and the scope suffix
// ("::scope") used for read. The errorLoop tracker keys on this coarse form
// so that repeated failing attempts with varied arguments on the same
// (tool, path) still accumulate into one streak.
func coarseOp(op string) string {
	if before, _, ok := strings.Cut(op, "#"); ok {
		op = before
	}
	if before, _, ok := strings.Cut(op, "::"); ok {
		op = before
	}
	return op
}

// describeScope turns a scope string into a human-readable phrase.
func describeScope(scope string, zh bool) string {
	if scope == "" {
		if zh {
			return "整个文件"
		}
		return "entire file"
	}
	if strings.HasPrefix(scope, "symbol:") {
		name := scope[len("symbol:"):]
		if zh {
			return name + " 方法"
		}
		return name + " symbol"
	}
	if zh {
		return "第 " + scope + " 行区间"
	}
	return "lines " + scope
}
