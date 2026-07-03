package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/deepact/deepact/skill"
	dlog "github.com/deepact/deepact/internal/log"
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
	Policy      PolicyChecker
	Context     ContextBuilder
	Compressor  Compressor
	Session     SessionStore
	Agents      *AgentRegistry
	Skills      *skill.Registry
	Router      ModelRouter
	MCPManagers []io.Closer // MCP server connections to close on shutdown
}

type Engine struct {
	model      ModelClient
	tools      ToolExecutor
	policy     PolicyChecker
	context    ContextBuilder
	compressor Compressor
	session    SessionStore
	agents     *AgentRegistry
	skills     *skill.Registry
	router     ModelRouter
	config     EngineConfig
	state      *TaskState
	history    []Message
	guards     *GuardSystem
	readLoop   *ReadLoopState
	errorLoop  *ErrorLoopState
	evalStore  EvalStore

	// pendingPinnedMessages holds messages (e.g., skill activations) that should
	// be appended at the END of the assembled messages array for the current
	// Run() call, rather than mixed into e.history. This preserves the stable
	// prefix cache across turns — history only grows with actual conversation.
	pendingPinnedMessages []string

	// matchedSkillsContent holds the content of matched skills for the current Run() call.
	// It is injected into sub-agent context when a handoff occurs, so skill methodology
	// instructions are carried through to sub-agents.
	matchedSkillsContent string

	// activatedSkills tracks skill names that have been explicitly activated
	// via /skill command within the current session, to prevent duplicate
	// injection from keyword-based auto-matching.
	activatedSkills map[string]bool

	// lastActivatedSkill records the most recently activated skill name.
	// The activate_skill tool checks NextSkills of this skill to determine
	// if auto-activation (no user confirmation) is allowed.
	lastActivatedSkill string

	// tddPhase tracks the current TDD phase when test-driven-development skill is active.
	// Phases: "" (inactive), "red", "red_verify", "green", "green_verify", "refactor".
	tddPhase       string
	tddPhaseDetail string

	// pendingEditPlan holds the agent's proposed edits for user confirmation.
	// When non-nil, the agent has proposed file modifications and is awaiting
	// user approval before execution.
	pendingEditPlan *PendingEditPlan

	// roundtableHall orchestrates multi-stance roundtable discussions.
	roundtableHall *RoundtableHall

	// Per-Run efficiency tracking
	runStartAt       time.Time
	runUsageAccum    ModelUsage
	usageMu          sync.Mutex // protects runUsageAccum from concurrent sub-agent goroutines
	runToolCallCount int
	runErrorCount    int

	// isChinese is set once from the first user message in the session.
	// All per-turn UI messages (skill list, activation prompts, etc.) use
	// this instead of recomputing msgIsChinese per-turn, which would switch
	// to English when the user types "ok"/"yes" to confirm.
	isChinese    bool
	langDetected bool
}

// PendingEditPlan captures the agent's proposed changes before execution.
// The agent's reasoning and planned edits are presented to the user for approval.
type PendingEditPlan struct {
	Reasoning string               // agent's explanation of what it understands
	Edits     []PendingEditAction  // individual file changes proposed
	Calls     []ToolCallRequest    // stored tool calls to execute on confirmation
	State     *TaskState           // snapshot of task state at proposal time
}

// PendingEditAction describes a single proposed file change.
type PendingEditAction struct {
	Tool     string `json:"tool"`     // "edit" or "write"
	Path     string `json:"path"`     // target file
	Summary  string `json:"summary"`  // human-readable description of the change
	OldText  string `json:"old,omitempty"`  // for edit: what will be replaced
	NewText  string `json:"new,omitempty"`  // what will be written
}

func NewEngine(cfg EngineConfig, deps EngineDeps) *Engine {
	guard := &GuardSystem{
		scope: NewScopeGuard(cfg.AutoConfirmScope),
		loop:  NewLoopGuard(cfg.WorkDir, 6), // block after 6 repeats of same (tool, path)
	}
	e := &Engine{
		model:      deps.Model,
		tools:      deps.Tools,
		policy:     deps.Policy,
		context:    deps.Context,
		compressor: deps.Compressor,
		session:    deps.Session,
		agents:     deps.Agents,
		skills:    deps.Skills,
		router:    deps.Router,
		config:     cfg,
		state:           &TaskState{TaskID: cfg.SessionID},
		history:         make([]Message, 0),
		guards:          guard,
		readLoop:        NewReadLoopState(),
		errorLoop:       NewErrorLoopState(0),
		activatedSkills: make(map[string]bool),
	}
	e.roundtableHall = NewRoundtableHall(e)

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

func (e *Engine) Run(ctx context.Context, userMsg string) (*EngineResponse, error) {
	if e.state == nil {
		return nil, fmt.Errorf("state not initialized")
	}
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

	// Reset per-Run state
	if e.guards.loop != nil {
		e.guards.loop.Reset()
	}
	if e.readLoop != nil {
		e.readLoop.Reset()
	}
	if e.errorLoop != nil {
		e.errorLoop.Reset()
	}
	e.matchedSkillsContent = ""
	e.tddPhase = ""
	e.tddPhaseDetail = ""
	e.runStartAt = time.Now()
	e.runUsageAccum = ModelUsage{}
	e.runToolCallCount = 0
	e.runErrorCount = 0

	// Team command handling — /team <goal>
	// Activates the debate arena: 4-round structured debate → user verdict.
	if tc := parseTeamCommand(userMsg); tc != nil {
		e.state.Roundtable = &RoundtableState{
			Goal:  tc.Goal,
			Phase: RoundtableProposal,
		}
		// Resolve members: command-line > config > defaults
		if len(tc.MemberIDs) > 0 {
			resolved := resolveMembers(tc.MemberIDs, DefaultDebateMembers)
			if len(resolved) > 0 {
				e.state.Roundtable.Members = resolved
			}
		}
		// Replace raw "/team <goal>" so the main agent loop sees a proper prompt
		if len(e.history) > 0 {
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"辩论模式已启动：%s\n\n请等待团队成员完成辩论。",
				tc.Goal)
			userMsg = fmt.Sprintf("辩论模式已启动：%s\n\n请等待团队成员完成辩论。", tc.Goal)
		}
	}

	// Skill command handling — /skills (list) and /skill <name> (activate)
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
				b.WriteString("\n使用 `/<名称>` 激活指定技能。")
			} else {
				b.WriteString("\nUse `/<name>` to activate a specific skill.")
			}
			return &EngineResponse{Summary: b.String(), Stage: StageAct}, nil

		case "activate":
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
			// Mark as explicitly activated to prevent duplicate auto-match
			e.activatedSkills[s.Name] = true
			e.lastActivatedSkill = s.Name
			e.state.ActiveSkillName = s.Name
			e.state.ActiveSkillContent = s.Content

			// Inject skill methodology into stable zone (persistent across turns)
			e.context.SetActiveSkill(s.Name, s.Content)

			// Brief one-time notification for this turn only
			skillMsg := fmt.Sprintf(
				"✅ Skill `%s` activated: %s. Full methodology now in stable zone.",
				s.Name, s.Description,
			)
			e.pendingPinnedMessages = append(e.pendingPinnedMessages, skillMsg)
			e.matchedSkillsContent = fmt.Sprintf("[SKILL — %s]\n\n%s", s.Name, s.Content)

			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{
					Type:   "skill_activated",
					Name:   s.Name,
					Detail: s.Description,
				})
			}

			taskText := extractTaskTextAfterSkillCmd(userMsg, sc.name)
			if taskText == "" {
				msg := fmt.Sprintf("✅ Skill `%s` activated: %s", s.Name, s.Description)
				if zh {
					msg = fmt.Sprintf("✅ 已激活 skill `%s`: %s", s.Name, s.Description)
				}
				return &EngineResponse{Summary: msg, Stage: StageAct}, nil
			}
			if len(e.history) > 0 {
				e.history[len(e.history)-1].Content = taskText
			}
		}
	}

	// Keyword-based skill matching.
	// 1. First pass: check for auto-activation (threshold met) — no user/model confirmation.
	// 2. Second pass: show suggestions so the model can activate_skill manually.
	if e.state.ActiveSkillName == "" {
		matches := e.skills.MatchTopSkillsWithScores(3, userMsg)

		// Auto-activation: if a skill's keyword match count >= threshold, activate it directly.
		var autoActivated *skill.Skill
		for _, m := range matches {
			if m.Skill.AutoActivateThreshold != nil && m.Score >= *m.Skill.AutoActivateThreshold {
				if !e.activatedSkills[m.Skill.Name] {
					autoActivated = m.Skill
					break
				}
			}
		}
		if autoActivated != nil {
			e.activatedSkills[autoActivated.Name] = true
			e.lastActivatedSkill = autoActivated.Name
			e.state.ActiveSkillName = autoActivated.Name
			e.state.ActiveSkillContent = autoActivated.Content

			// Inject skill methodology into stable zone (persistent across turns)
			e.context.SetActiveSkill(autoActivated.Name, autoActivated.Content)

			skillMsg := fmt.Sprintf(
				"✅ Skill `%s` auto-activated (keyword score %d). Full methodology now in stable zone.",
				autoActivated.Name, matches[0].Score,
			)
			e.pendingPinnedMessages = append(e.pendingPinnedMessages, skillMsg)
			e.matchedSkillsContent = fmt.Sprintf("[SKILL — %s]\n\n%s", autoActivated.Name, autoActivated.Content)
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{
					Type:   "skill_activated",
					Name:   autoActivated.Name,
					Detail: autoActivated.Description + " (auto, keyword match)",
				})
			}
		}

		// Suggestion: show matched skills as suggestions for model-driven activation.
		if autoActivated == nil && len(matches) > 0 {
			var sb strings.Builder
			if zh {
				sb.WriteString("## 建议的技能\n以下技能可能适合当前任务：\n\n")
			} else {
				sb.WriteString("## Suggested Skills\nSkills that may be relevant:\n\n")
			}
			for _, m := range matches {
				sb.WriteString(fmt.Sprintf("- **%s**: %s\n", m.Skill.Name, m.Skill.Description))
			}
			if zh {
				sb.WriteString("\n使用 `/<skillname>` 激活，或让模型用 `activate_skill` tool 建议。")
			} else {
				sb.WriteString("\nUse `/<skillname>` to activate, or ask the model to suggest via `activate_skill` tool.")
			}
			e.pendingPinnedMessages = append(e.pendingPinnedMessages, sb.String())
		}
	}

	// /clear command — reset all task state and wait for new input.
	if isClearCommand(userMsg) {
		e.clearSessionState()
		msg := "✅ 状态已清理。请提出新的问题。"
		if !zh {
			msg = "✅ State cleared. Please ask a new question."
		}
		return &EngineResponse{Summary: msg, Stage: StageAct}, nil
	}

	// Debate Arena phase — execute the current debate round, then return
	// the round result to the user. The engine continues to the next round
	// on the next Run() call until AwaitingVerdict.
	if e.state.Roundtable != nil {
		phase := e.state.Roundtable.Phase
		switch phase {
		case RoundtableProposal, RoundtableChallenge, RoundtableRebuttal, RoundtableFinal:
			response, err := e.roundtableHall.handleDebateArena(ctx)
			if err != nil {
				return nil, fmt.Errorf("debate arena: %w", err)
			}
			if response != nil {
				return response, nil
			}
		case RoundtableAwaitingVerdict:
			response, err := e.roundtableHall.Advance(ctx, userMsg)
			if err != nil {
				return nil, fmt.Errorf("verdict: %w", err)
			}
			if response != nil {
				return response, nil
			}
		case RoundtableDone:
			// Debate complete — clear roundtable state so normal flow resumes.
			// The verdict was already injected as a pinned message.
			e.state.Roundtable = nil
		}
	}

	e.updateGoalFromFirstMessage(userMsg)

	if e.pendingEditPlan != nil {
		if !isDangerousConfirmation(userMsg) {
			// User is providing feedback/instruction on the proposed plan, not confirming it.
			// Contextualize the user message so the LLM understands this is plan feedback
			// and can revise its approach, rather than regenerating the same edits.
			if len(e.history) > 0 && e.history[len(e.history)-1].Role == "user" {
				if e.isChinese {
					e.history[len(e.history)-1].Content = fmt.Sprintf(
						"用户对之前提出的修改方案给出了反馈：%s\n\n请根据用户反馈重新思考并决定下一步做什么。如果用户要求修改方案，请提出更新后的方案。",
						userMsg,
					)
				} else {
					e.history[len(e.history)-1].Content = fmt.Sprintf(
						"The user provided feedback on the previously proposed edit plan: %s\n\nReassess and decide what to do next. If the user requested changes, propose a revised plan.",
						userMsg,
					)
				}
			}
			e.pendingEditPlan = nil
			e.state.PlanConfirmed = false
		}
	}

	// Phase 1: Edit plan confirmed — execute directly with progressive diff display
	if e.pendingEditPlan != nil && isDangerousConfirmation(userMsg) {
		zh := e.isChinese
		plan := e.pendingEditPlan
		e.pendingEditPlan = nil

		if plan.State != nil {
			*e.state = *plan.State
		}
		e.state.PlanConfirmed = true
		e.state.ConfirmedScope = true

		msg := "✅ 方案已确认，开始执行..."
		if !zh {
			msg = "✅ Plan confirmed, executing..."
		}
		e.history = append(e.history, Message{Role: "user", Content: msg, Timestamp: time.Now()})

		// Re-emit the assistant message with tool_calls
		assistantMsg := Message{
			Role:      "assistant",
			Content:   plan.Reasoning,
			Timestamp: time.Now(),
		}
		assistantMsg.ToolCalls = make([]MessageToolCall, 0, len(plan.Calls))
		for _, c := range plan.Calls {
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, MessageToolCall{
				ID:        c.ID,
				Name:      c.Name,
				Arguments: string(c.Input),
			})
		}
		e.history = append(e.history, assistantMsg)

		// Execute the stored calls.
		// read/grep/glob calls are intentionally skipped here (their results were
		// already consumed when the plan was first proposed). But their IDs were
		// still emitted as tool_calls in the assistant message above, so DeepSeek
		// requires a tool message for each of them. We append placeholder tool
		// messages to satisfy the "assistant(tool_calls) → tool" contract —
		// otherwise the API returns 400 "insufficient tool messages following
		// tool_calls message".
		var handoffCalls, regularCalls []ToolCallRequest
		for _, c := range plan.Calls {
			switch c.Name {
			case HandoffToolName:
				handoffCalls = append(handoffCalls, c)
			case "read", "grep", "glob":
				e.history = append(e.history, Message{
					Role:       "tool",
					ToolCallID: c.ID,
					Content:    "Skipped: read-only call already consumed before plan confirmation.",
					Timestamp:  time.Now(),
				})
			default:
				regularCalls = append(regularCalls, c)
			}
		}

		// Execute handoff calls — parallel when multiple, sequential when single.
		if len(handoffCalls) > 0 {
			results := e.executeHandoffsParallel(ctx, handoffCalls)
			for i, call := range handoffCalls {
				result := results[i]

				// Hard gate: if critic returns FAIL, intercept and present to user.
				if isCriticHandoff(call.Input) && parseCriticVerdict(result.Digest) == "FAIL" {
					e.history = append(e.history, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()})
					zh := e.isChinese
					return &EngineResponse{
						Summary: buildCriticFailSummary(result.Digest, zh),
						Stage:   StageVerifyFailed,
					}, nil
				}

				e.history = append(e.history, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()})
			}
		}

		// Execute regular calls with progressive UI (read-only batched, destructive sequential)
		if len(regularCalls) > 0 {
			var readOnlyCalls, destructiveCalls []ToolCallRequest
			for _, call := range regularCalls {
				if call.Name == "edit" || call.Name == "write" {
					destructiveCalls = append(destructiveCalls, call)
				} else {
					readOnlyCalls = append(readOnlyCalls, call)
				}
			}

			// Batch read-only tools
			if len(readOnlyCalls) > 0 {
				for _, call := range readOnlyCalls {
					if e.config.OnProgress != nil {
						e.config.OnProgress(ProgressEvent{Type: "tool_start", Name: call.Name, Detail: summarizeArgs(call.Name, call.Input, e.config.WorkDir)})
					}
				}
				roResults := e.tools.Execute(ToolExecContext{WorkDir: e.config.WorkDir, SessionID: e.config.SessionID, TurnNumber: e.state.TurnNumber}, readOnlyCalls)
				for _, result := range roResults {
					if e.config.OnProgress != nil {
						e.config.OnProgress(ProgressEvent{Type: "tool_done", Name: result.ToolName, Detail: briefDigest(result.Digest), FullDetail: result.Digest})
					}
					e.history = append(e.history, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()})
				}
			}

			// Sequential destructive tools with diff display
			for _, call := range destructiveCalls {
				if e.config.OnProgress != nil {
					e.config.OnProgress(ProgressEvent{Type: "tool_start", Name: call.Name, Detail: summarizeArgs(call.Name, call.Input, e.config.WorkDir)})
				}
				results := e.tools.Execute(ToolExecContext{WorkDir: e.config.WorkDir, SessionID: e.config.SessionID, TurnNumber: e.state.TurnNumber}, []ToolCallRequest{call})
				if len(results) > 0 {
					result := results[0]
					if e.config.OnProgress != nil {
						e.config.OnProgress(ProgressEvent{Type: "tool_done", Name: result.ToolName, Detail: briefDigest(result.Digest), FullDetail: result.Digest})
					}
					e.history = append(e.history, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()})
				}
			}

			allCalls := append(readOnlyCalls, destructiveCalls...)
			allResults := make([]ToolResult, 0)
			for i := len(e.history) - len(regularCalls); i < len(e.history); i++ {
				if i >= 0 && e.history[i].Role == "tool" {
					allResults = append(allResults, ToolResult{ToolCallID: e.history[i].ToolCallID, Digest: e.history[i].Content})
				}
			}
			e.updateTaskStateFromTools(allCalls, allResults)
		}
		// Fall through to the agent loop — the agent can see tool results
		// and decide if further changes are needed.
	}

	// Dangerous command confirmation — simple exact match, safety feature only
	if e.state.PendingDangerousCmd != "" && isDangerousConfirmation(userMsg) {
		confirmedCmd := e.state.PendingDangerousCmd
		e.guards.scope.ConfirmDangerous(e.state.PendingDangerousCmd)
		e.state.PendingDangerousCmd = ""
		msg := "✅ Dangerous command confirmed, proceeding..."
		if zh {
			msg = "✅ 危险命令已确认，继续执行..."
		}
		e.history = append(e.history, Message{Role: "user", Content: msg, Timestamp: time.Now()})
		// Tell the agent to re-issue the blocked command.
		reissueHint := fmt.Sprintf("用户已确认执行危险命令。请重新执行之前被阻断的命令: `%s`", confirmedCmd)
		if !zh {
			reissueHint = fmt.Sprintf("The user confirmed the dangerous command. Please re-issue the previously blocked command: `%s`", confirmedCmd)
		}
		e.history = append(e.history, Message{Role: "user", Content: reissueHint, Timestamp: time.Now()})
	}

	// Auto-deactivate skill when user intent shifts from development to operational use.
	// This prevents skill methodology (e.g., TDD) from constraining verification
	// or ad-hoc testing after development is complete.
	if e.state.ActiveSkillName != "" && !strings.HasPrefix(strings.TrimSpace(userMsg), "/") {
		if e.detectIntentShift(userMsg) {
			skillName := e.state.ActiveSkillName
			e.deactivateSkill()
			msg := fmt.Sprintf("✅ 自动解除 skill `%s`：检测到意图从开发转向使用/验证。", skillName)
			if !zh {
				msg = fmt.Sprintf("✅ Auto-deactivated skill `%s`: intent shift from development to usage/verification.", skillName)
			}
			e.history = append(e.history, Message{Role: "user", Content: msg, Timestamp: time.Now()})
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{
					Type:   "skill_deactivated",
					Name:   skillName,
					Detail: "auto-deactivated due to intent shift",
				})
			}
			loopLog.Printf("auto-deactivated skill %q: user intent shift detected", skillName)
		}
	}

	// Detect user intent: analysis-only vs new-topic vs continue.
	// Resets PlanConfirmed when the user starts a new topic or asks for
	// analysis only, preventing edit-plan-guard bypass across Run() calls.
	intent := e.detectUserIntent(userMsg)
	switch intent {
	case IntentAnalyze:
		e.state.PlanConfirmed = false
		constraint := "[ANALYSIS MODE] 用户要求仅进行分析，不要修改任何代码。你的任务仅限于：阅读代码、分析原因、解释行为。禁止：edit、write、或任何修改文件的操作。"
		if !zh {
			constraint = "[ANALYSIS MODE] The user asked for analysis only. Do NOT modify any code. Your task is limited to: reading code, analyzing causes, explaining behavior. FORBIDDEN: edit, write, or any file modification operations."
		}
		e.pendingPinnedMessages = append(e.pendingPinnedMessages, constraint)
		loopLog.Printf("intent: analyze-only, reset PlanConfirmed + injected constraint")
	case IntentNewTopic:
		e.state.PlanConfirmed = false
		loopLog.Printf("intent: new topic, reset PlanConfirmed (was %q)", e.state.Goal)
	default: // IntentContinue
		loopLog.Printf("intent: continue, keeping PlanConfirmed=%v", e.state.PlanConfirmed)
	}

	// Scope is implicitly confirmed when user sends any message
	if !e.state.ConfirmedScope {
		e.state.ConfirmedScope = true
	}

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
		if turnResult.VerifyFailedSummary != "" {
			return &EngineResponse{
				Summary: turnResult.VerifyFailedSummary,
				Stage:   StageVerifyFailed,
			}, nil
		}
		if turnResult.Blocked {
			e.runErrorCount++
			return &EngineResponse{Questions: turnResult.Questions, Stage: StageAct, Blocked: true, BlockedBy: turnResult.BlockedBy, FinishReason: turnResult.FinishReason}, nil
		}
		if turnResult.Done {
			break
		}

		// Loop detection: read ops go through ReadLoopState (two-tier:
		// 3rd same (path,scope) → nudge, 4th → block). Non-read ops keep the
		// original consecutiveSameOp guard (5 consecutive same first-calls →
		// block), which covers tools LoopGuard doesn't track (grep/bash/etc.).
		if turnResult.LastOp != "" {
			if strings.HasPrefix(turnResult.LastOp, "read:") {
				action := e.readLoop.Check(turnResult.LastOp)
				switch action.Type {
				case GuardDiagnose:
					nudge := buildReadLoopNudge(turnResult.LastOp, zh)
					e.history = append(e.history, Message{
						Role:    "user",
						Content: nudge,
					})
					loopLog.Printf("read-loop nudge injected for %s", turnResult.LastOp)
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
				// unlike the content-hash-based LoopGuard/consecutiveSameOp.
				if e.errorLoop != nil {
					action := e.errorLoop.Check(coarseOp(turnResult.LastOp), turnResult.LastOpError)
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
		turns++
	}
	// Advance session turn counter past the last executed turn so the next
	// Run() call continues from the correct position. +1 because 'turns' was
	// not incremented after a Done break — it still points to the completed turn.
	e.state.TurnNumber = turns + 1

	// Clean up completed roundtable state. It was available in Block B for this
	// Run() call's context; subsequent turns don't need stale roundtable data.
	if e.state.Roundtable != nil && e.state.Roundtable.Phase == RoundtableDone {
		e.state.Roundtable = nil
	}

	if err := e.emitEvent("act_complete", StageAct, nil); err != nil {
		return nil, err
	}

	if err := e.verifyAndCompact(); err != nil {
		return nil, err
	}

	// Record efficiency eval at end of Run()
	e.recordRunEval(zh)

	summary := buildRunSummary(e.history, e.runToolCallCount, zh)
	loopLog.Printf("Run done: turns=%d total=%s tool_calls=%d errors=%d usage prompt=%d completion=%d cache_hit=%d cache_miss=%d",
		e.state.TurnNumber, time.Since(e.runStartAt), e.runToolCallCount, e.runErrorCount,
		e.runUsageAccum.PromptTokens, e.runUsageAccum.CompletionTokens,
		e.runUsageAccum.CacheHitTokens, e.runUsageAccum.CacheMissTokens)
	return &EngineResponse{Summary: summary, Stage: StageVerifyCompact}, nil
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
func buildRunSummary(history []Message, toolCallCount int, zh bool) string {
	summary := ""
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" && history[i].Content != "" {
			summary = history[i].Content
			break
		}
	}
	if summary == "" {
		for i := len(history) - 1; i >= 0; i-- {
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

// isDangerousConfirmation is a narrow safety gate for dangerous command approval.
// Only exact matches — this is a safety feature, not fuzzy intent detection.
func isDangerousConfirmation(msg string) bool {
	normalized := strings.ToLower(strings.TrimSpace(msg))
	switch normalized {
	case "yes", "y", "ok", "okay", "confirm", "proceed", "go", "do it", "sure", "yep",
		"同意", "确认", "是", "执行", "可以", "好的", "好", "行",
		"对", "对的", "没问题", "嗯", "开始", "改", "改吧", "做", "做吧", "来", "来吧", "干", "干吧", "去吧":
		return true
	}
	// Exact compound phrases users naturally type in reply to "确认执行修改？".
	// "修改" is not a generic confirm word (it is ambiguous on its own), so these
	// are enumerated explicitly rather than handled by isConcatOfConfirmWords.
	switch normalized {
	case "确认执行修改", "确认修改", "执行修改":
		return true
	}
	// Handle compound confirmations like "对，改吧" or "好的，执行"
	for _, sep := range []string{"，", ",", " ", "、"} {
		if strings.Contains(normalized, sep) {
			parts := strings.Split(normalized, sep)
			allConfirm := true
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				if !isSingleConfirmWord(p) {
					allConfirm = false
					break
				}
			}
			if allConfirm {
				return true
			}
		}
	}
	// Handle concatenations of confirm words with NO separator, e.g. "确认执行",
	// "确认执行修改", "继续执行". Without this, a user replying "确认执行" to the
	// "确认执行修改？" prompt is treated as plan feedback rather than confirmation,
	// discarding the pending edit plan and re-proposing it forever.
	if isConcatOfConfirmWords(normalized) {
		return true
	}
	return false
}

// isConcatOfConfirmWords reports whether s is composed entirely of known single
// confirmation words concatenated without separators (e.g. "确认执行" = "确认" +
// "执行"). The whole string must be consumed — a real instruction like "确认但改下方案"
// never matches, so this stays a safe affirmative gate.
func isConcatOfConfirmWords(s string) bool {
	if s == "" {
		return false
	}
	runes := []rune(s)
	n := len(runes)
	// dp[i] is true if runes[i:] can be fully segmented into confirm words.
	dp := make([]bool, n+1)
	dp[n] = true
	for i := n - 1; i >= 0; i-- {
		for j := i + 1; j <= n; j++ {
			if dp[j] && isSingleConfirmWord(string(runes[i:j])) {
				dp[i] = true
				break
			}
		}
	}
	return dp[0]
}

func isSingleConfirmWord(word string) bool {
	switch word {
	case "yes", "y", "ok", "okay", "confirm", "proceed", "go", "do", "it", "sure", "yep",
		"同意", "确认", "是", "执行", "可以", "好的", "好", "行",
		"对", "对的", "没问题", "嗯", "开始", "改", "改吧", "做", "做吧", "来", "来吧", "干", "干吧", "去吧", "吧",
		"继续":
		return true
	}
	return false
}

func (e *Engine) emitEvent(eventType string, stage Stage, payload any) error {
	if e.session == nil {
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	event := Event{SessionID: e.config.SessionID, Type: eventType, Stage: stage, Timestamp: time.Now(), Payload: data}
	if err := e.session.AppendEvent(event); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

func (e *Engine) verifyAndCompact() error {
	if e.context == nil || e.compressor == nil {
		return nil
	}
	messages := e.context.Build(e.state, e.history, nil)
	tokens := e.context.EstimateTokens(messages)
	layer, should := e.compressor.ShouldCompress(tokens, e.config.MaxContextTokens)
	if should {
		compacted, err := e.compressor.Compress(layer, e.state, e.history)
		if err != nil {
			return fmt.Errorf("compress: %w", err)
		}
		e.history = compacted
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

// detectIntentShift checks if the user's message signals an intent shift from
// "development/implementation" to "operational use/verification" of existing work.
// When detected, the active skill should be auto-deactivated so its methodology
// no longer constrains the agent's behavior.
//
// Heuristics:
//   - "用这个/拿这个/试试这个 X" pattern: user wants to USE/TRY existing code
//   - Operational intent (看看/试试/跑一下/检查/验证) without development intent (写/实现/开发/添加)
func (e *Engine) detectIntentShift(userMsg string) bool {
	msg := strings.ToLower(userMsg)

	// Strong shift signals: user wants to apply something to existing work
	strongShiftPhrases := []string{
		"用这个",   // "use this..."
		"拿这个",   // "take this..."
		"试试这个", // "try this..."
		"用这个token",
		"用这个key",
		"用这个密钥",
	}
	for _, p := range strongShiftPhrases {
		if strings.Contains(msg, p) {
			return true
		}
	}

	// General heuristic: operational intent without development intent
	// Development keywords indicate the user is still building/implementing
	devWords := []string{"写", "实现", "开发", "添加", "增加", "创建", "修改", "重构", "设计", "建一个"}
	// Operational keywords indicate the user wants to use/verify existing work
	opWords := []string{
		"看看", "看一下", "看一看", "检查", "检查一下", "验证", "验证一下",
		"试试", "试一下", "测试一下",
		"跑一下", "跑起来", "启动", "运行", "运行一下",
		"看看结果", "看看效果",
	}

	hasDev := false
	for _, w := range devWords {
		if strings.Contains(msg, w) {
			hasDev = true
			break
		}
	}
	hasOp := false
	for _, w := range opWords {
		if strings.Contains(msg, w) {
			hasOp = true
			break
		}
	}

	return hasOp && !hasDev
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

// deactivateSkill clears the active skill state, releasing the agent from
// the skill's methodology constraints. If the current skill has NextSkills,
// the first next skill in the chain is auto-activated, ensuring the skill
// chain (e.g., brainstorming → writing-plans → TDD) is followed without
// requiring the model to manually call activate_skill.
func (e *Engine) deactivateSkill() {
	currentName := e.state.ActiveSkillName
	if currentName == "" {
		return
	}

	// Look up current skill's NextSkills for chain auto-activation
	var nextSkill *skill.Skill
	if e.skills != nil {
		if current := e.skills.Get(currentName); current != nil && len(current.NextSkills) > 0 {
			nextName := current.NextSkills[0]
			if nextName != "" && nextName != currentName {
				nextSkill = e.skills.Get(nextName)
			}
		}
	}

	e.state.ActiveSkillName = ""
	e.state.ActiveSkillContent = ""
	e.matchedSkillsContent = ""
	e.context.SetActiveSkill("", "")
	// Keep lastActivatedSkill for chain tracking purposes
	// Keep activatedSkills map for deduplication purposes
	// Reset TDD-specific phase tracking
	e.tddPhase = ""
	e.tddPhaseDetail = ""

	// Auto-activate next skill in chain
	if nextSkill != nil {
		e.activatedSkills[nextSkill.Name] = true
		e.lastActivatedSkill = nextSkill.Name
		e.state.ActiveSkillName = nextSkill.Name
		e.state.ActiveSkillContent = nextSkill.Content
		e.context.SetActiveSkill(nextSkill.Name, nextSkill.Content)
		e.matchedSkillsContent = fmt.Sprintf("[SKILL — %s]\n\n%s", nextSkill.Name, nextSkill.Content)

		chainInfo := fmt.Sprintf(" (chain: %s → %s)", currentName, nextSkill.Name)
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{
				Type:   "skill_activated",
				Name:   nextSkill.Name,
				Detail: nextSkill.Description + chainInfo,
			})
		}

		zh := e.isChinese
		msg := fmt.Sprintf("✅ Skill `%s` auto-activated%s. Full methodology now in stable zone.", nextSkill.Name, chainInfo)
		if zh {
			msg = fmt.Sprintf("✅ 已自动激活 skill `%s`%s。方法论已注入稳定区。", nextSkill.Name, chainInfo)
		}
		e.pendingPinnedMessages = append(e.pendingPinnedMessages, msg)

		loopLog.Printf("skill chain: %s → %s auto-activated", currentName, nextSkill.Name)
	}
}

func msgIsChinese(msg string) bool {
	for _, r := range msg {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func joinSkillNames(skills []*skill.Skill) string {
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

// skillCommand represents a parsed /skill or /skills command.
type skillCommand struct {
	action string // "list" or "activate"
	name   string // skill name for "activate"
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
	if cmd == "clear" {
		return nil
	}

	switch cmd {
	case "skills":
		return &skillCommand{action: "list"}
	case "skill":
		if len(parts) < 2 {
			return &skillCommand{action: "list"}
		}
		return &skillCommand{action: "activate", name: strings.ToLower(parts[1])}
	default:
		if isValidSkillName(cmd) {
			return &skillCommand{action: "activate", name: cmd}
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

// detectUserIntent classifies the user's message intent for PlanConfirmed management.
// Detection order (strongest signal first):
//  1. Analysis-only: explicit analysis request without modification command → IntentAnalyze
//  2. Context reference: explicitly continues previous work → IntentContinue
//  3. Topic continuity: shares key terms with current goal → IntentContinue
//  4. Default: new topic → IntentNewTopic
func (e *Engine) detectUserIntent(userMsg string) UserIntent {
	if e.state == nil {
		return IntentContinue
	}

	// If no prior goal, treat as first interaction or post-clear.
	if e.state.Goal == "" {
		return IntentContinue
	}

	msg := strings.ToLower(strings.TrimSpace(userMsg))

	// A pure confirmation (e.g. "确认", "确认执行", "yes") continues the current
	// task. It must NOT be classified as a new topic: otherwise PlanConfirmed is
	// reset below and the edit-plan guard re-triggers on the next turn, trapping
	// the user in a "确认执行修改？" loop even after they already approved.
	if isDangerousConfirmation(msg) {
		return IntentContinue
	}

	// Signal 1: Analysis-only — user wants explanation, not modification.
	// Check BEFORE context reference, so "分析一下刚才那个问题" is still analysis-only.
	if isAnalysisOnly(msg) {
		return IntentAnalyze
	}

	// Signal 2: Explicit context reference — user continues previous work.
	if hasContextReference(msg) {
		return IntentContinue
	}

	// Signal 3: Topic continuity — check if key terms overlap with goal.
	if isSameTopic(msg, strings.ToLower(e.state.Goal)) {
		return IntentContinue
	}

	return IntentNewTopic
}

// hasContextReference checks if the message explicitly references previous work.
// Uses phrase-level patterns to avoid false positives from common deictic words
// like "这个"/"那个" used in normal description (e.g. "解释一下这个函数").
func hasContextReference(msg string) bool {
	refs := []string{
		// Chinese — explicit continuation of previous work
		"刚才", "上面", "之前", "刚刚", "继续", "接着",
		"也加", "也改", "也修", "也做", "也写", "也删", "也弄",
		"再改", "再修", "再做", "再加", "再删", "再调整",
		// English
		"also add", "also fix", "also change", "also update", "also remove",
		"additionally", "furthermore", "continue",
		"previous", "above",
	}
	for _, r := range refs {
		if strings.Contains(msg, r) {
			return true
		}
	}
	return false
}

// isAnalysisOnly checks if the message is a pure analysis/explanation request
// without any modification command intent. Uses phrase-level detection to avoid
// false positives from "修改"/"修复" appearing as descriptive nouns
// (e.g. "代码修改的diff区域" → analysis, not modification).
func isAnalysisOnly(msg string) bool {
	analysisPatterns := []string{
		// Chinese — question/analysis markers
		"为什么", "怎么", "如何", "是什么", "怎么回事", "为什么会",
		"是什么原因", "什么原因", "怎么看",
		"分析一下", "分析下", "分析",
		"解释一下", "解释下", "解释",
		"看看", "看一下", "看一看",
		"讲讲", "说一下", "讲一下", "说说",
		"帮我看看", "帮我分析", "帮我解释",
		// English
		"analyze", "explain", "why", "how come", "what is",
		"what are", "what's", "how does", "how do",
		"check", "look at", "examine", "investigate",
		"tell me about", "walk me through",
	}

	hasAnalysis := false
	for _, p := range analysisPatterns {
		if strings.Contains(msg, p) {
			hasAnalysis = true
			break
		}
	}
	if !hasAnalysis {
		return false
	}

	// Modification command phrases — stronger signals than single characters.
	// These indicate the user wants code CHANGED, not just analyzed.
	modPhrases := []string{
		// Chinese imperative modification phrases
		"改一下", "改下", "改吧", "改掉", "改成", "改为",
		"修一下", "修复一下", "修掉",
		"做一下", "弄一下", "搞一下",
		"加一下", "加一个", "加上", "加个",
		"删掉", "删除掉", "移除掉", "去掉",
		"换一下", "换成", "替换成",
		"然后改", "然后修", "再改", "再修",
		"并修改", "并更新", "并修复", "并改",
		"改一改", "修一修",
		"写一下", "写个", "写一个",
		// English imperative phrases
		"fix it", "fix the", "change it", "change the",
		"modify the", "update the", "replace the",
		"add a", "add the", "create a", "remove the",
	}
	for _, p := range modPhrases {
		if strings.Contains(msg, p) {
			return false
		}
	}

	// Bare modification characters (改/修/加/删/换/写) are strong signals of
	// modification intent, but they also appear in descriptive nouns
	// (e.g. "代码修改" in "为什么点击代码修改的diff区域没反应").
	// Strategy: if the message has STRONG analysis question words
	// ("为什么", "怎么", "是什么", "怎么回事"), treat bare chars as descriptive.
	// Otherwise, treat them as modification intent.
	bareModChars := []string{"改", "修", "加", "删", "换", "写"}
	strongAnalysisQuestion := false
	for _, w := range []string{"为什么", "怎么", "如何", "是什么", "怎么回事", "为什么会"} {
		if strings.Contains(msg, w) {
			strongAnalysisQuestion = true
			break
		}
	}
	if !strongAnalysisQuestion {
		for _, c := range bareModChars {
			if strings.Contains(msg, c) {
				return false
			}
		}
	}

	return true
}

// isSameTopic checks if the new message shares significant terms with the goal,
// indicating the user is continuing work on the same topic.
// Single key term overlap (e.g. "登录") is sufficient for Chinese;
// English requires 2+ overlaps or >25% goal term coverage.
func isSameTopic(msg, goal string) bool {
	msgTerms := extractKeyTerms(msg)
	goalTerms := extractKeyTerms(goal)

	if len(msgTerms) == 0 || len(goalTerms) == 0 {
		return false
	}

	overlap := 0
	for _, mt := range msgTerms {
		for _, gt := range goalTerms {
			if mt == gt || strings.Contains(mt, gt) || strings.Contains(gt, mt) {
				overlap++
				break
			}
		}
	}

	// For Chinese: single shared key term is a strong signal.
	// For English: require 2+ or 25% coverage.
	return overlap >= 1
}

// extractKeyTerms extracts key terms from a message for topic comparison.
// Chinese: extracts character bigrams as terms.
// English: extracts lowercase words ≥ 3 chars, skipping stop words.
func extractKeyTerms(text string) []string {
	stopWords := map[string]bool{
		"the": true, "and": true, "for": true, "that": true, "this": true,
		"with": true, "from": true, "have": true, "what": true, "when": true,
		"where": true, "which": true, "about": true, "does": true,
		"的": true, "了": true, "是": true, "在": true, "我": true,
		"有": true, "和": true, "就": true, "不": true, "人": true,
		"都": true, "一": true, "一个": true, "上": true, "也": true,
		"很": true, "到": true, "说": true, "要": true, "去": true,
		"你": true, "会": true, "着": true, "没有": true, "看": true,
		"好": true, "自己": true, "这": true, "他": true, "她": true,
		"它": true, "们": true, "那": true, "什么": true, "吗": true,
		"吧": true, "呢": true, "啊": true, "哦": true, "嗯": true,
	}

	var terms []string
	seen := make(map[string]bool)

	// Extract Chinese bigrams (skip single chars and stop words).
	runes := []rune(text)
	for i := 0; i < len(runes)-1; i++ {
		// Check if these are Chinese characters
		if isCJK(runes[i]) && isCJK(runes[i+1]) {
			bigram := string(runes[i : i+2])
			if !stopWords[bigram] && !seen[bigram] {
				terms = append(terms, bigram)
				seen[bigram] = true
			}
		}
	}

	// Extract English words ≥ 3 chars.
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !('a' <= r && r <= 'z') && !('A' <= r && r <= 'Z')
	})
	for _, w := range words {
		w = strings.ToLower(w)
		if len(w) >= 3 && !stopWords[w] && !seen[w] {
			terms = append(terms, w)
			seen[w] = true
		}
	}

	return terms
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Unified Ideographs Extension A
		(r >= 0x20000 && r <= 0x2A6DF) // CJK Unified Ideographs Extension B
}

// isClearCommand detects the /clear signal that resets all session state.
func isClearCommand(userMsg string) bool {
	trimmed := strings.TrimSpace(userMsg)
	return trimmed == "/clear" || strings.HasPrefix(trimmed, "/clear ")
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
	e.state.ConfirmedScope = false

	e.pendingEditPlan = nil

	e.deactivateSkill()
	e.activatedSkills = make(map[string]bool)
	e.lastActivatedSkill = ""
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
// ("::scope") used for read. ErrorLoopState keys on this coarse form so that
// repeated failing attempts with varied arguments on the same (tool, path)
// still accumulate into one streak.
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
