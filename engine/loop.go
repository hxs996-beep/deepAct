package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/deepact/deepact/skill"
	dlog "github.com/deepact/deepact/internal/log"
)

var loopLog = dlog.New("[loop] ")

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
	runToolCallCount int
	runErrorCount    int
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
		loop:  NewLoopGuard(6), // block after 6 repeats of same (tool, path)
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
	zh := msgIsChinese(userMsg)
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
	e.matchedSkillsContent = ""
	e.tddPhase = ""
	e.tddPhaseDetail = ""
	e.runStartAt = time.Now()
	e.runUsageAccum = ModelUsage{}
	e.runToolCallCount = 0
	e.runErrorCount = 0

	// Roundtable command handling — /round <goal>
	// Must be checked BEFORE parseSkillCommand, which would otherwise
	// treat "/round" as an unknown skill name.
	// No longer intercepts — creates state and lets the main agent
	// generate proposals in its normal loop.
	if rc := parseRoundtableCommand(userMsg); rc != nil {
		e.state.Roundtable = &RoundtableState{
			Goal:  rc.Goal,
			Phase: RoundtableExplore,
		}
		// Replace raw "/round <goal>" with a proper task prompt
		// so the main agent generates proposals in its normal loop.
		// Also override userMsg so subsequent skill parsing doesn't
		// see "/round" and treat it as an unknown skill.
		if len(e.history) > 0 {
			newContent := fmt.Sprintf(
				"需求：%s\n\n请生成 2-3 个不同的实现方案。\n\n每个方案请以「## 方案 N: 标题」开头，对每个方案说明：\n1. 方案思路\n2. 涉及的主要文件和改动\n3. 优缺点",
				rc.Goal)
			e.history[len(e.history)-1].Content = newContent
			userMsg = newContent
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

			skillMsg := fmt.Sprintf(
				"[SKILL ACTIVATED: %s]\n\nThe following methodology has been activated per user request. Follow it precisely.\n\n%s",
				s.Name, s.Content,
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

	// Keyword-based skill suggestion — no auto-activation.
	// Match top skills and suggest them as a block so the model can decide
	// whether to activate one via the activate_skill tool.
	if e.state.ActiveSkillName == "" {
		if matched := e.skills.MatchTopSkills(3, userMsg); len(matched) > 0 {
			var sb strings.Builder
			if zh {
				sb.WriteString("## 建议的技能\n以下技能可能适合当前任务：\n\n")
			} else {
				sb.WriteString("## Suggested Skills\nSkills that may be relevant:\n\n")
			}
			for _, s := range matched {
				sb.WriteString(fmt.Sprintf("- **%s**: %s\n", s.Name, s.Description))
			}
			if zh {
				sb.WriteString("\n使用 `/<skillname>` 激活，或让模型用 `activate_skill` tool 建议。")
			} else {
				sb.WriteString("\nUse `/<skillname>` to activate, or ask the model to suggest via `activate_skill` tool.")
			}
			e.pendingPinnedMessages = append(e.pendingPinnedMessages, sb.String())
		}
	}

	// Roundtable command handling — /round <goal> (post-skill check)
	// Same as above: create state, replace message, fall through.
	if rc := parseRoundtableCommand(userMsg); rc != nil {
		if e.state.Roundtable == nil {
			e.state.Roundtable = &RoundtableState{
				Goal:  rc.Goal,
				Phase: RoundtableExplore,
			}
			if len(e.history) > 0 {
				e.history[len(e.history)-1].Content = fmt.Sprintf(
					"需求：%s\n\n请生成 2-3 个不同的实现方案。\n\n每个方案请以「## 方案 N: 标题」开头，对每个方案说明：\n1. 方案思路\n2. 涉及的主要文件和改动\n3. 优缺点",
					rc.Goal)
			}
		}
	}

	// Roundtable active — only intercept in Review phase (user triggered "都评一下")
	if e.state.Roundtable != nil && e.state.Roundtable.Phase == RoundtableReview {
		response, err := e.roundtableHall.Advance(ctx, userMsg)
		if err != nil {
			return nil, fmt.Errorf("roundtable advance: %w", err)
		}
		if response != nil {
			return response, nil
		}
	}

	e.updateGoalFromFirstMessage(userMsg)

	if e.pendingEditPlan != nil {
		if !isDangerousConfirmation(userMsg) {
			e.pendingEditPlan = nil
			e.state.PlanConfirmed = false
		}
	}

	// Edit plan confirmation — user approved the agent's proposed changes
	if e.pendingEditPlan != nil && isDangerousConfirmation(userMsg) {
		zh := msgIsChinese(userMsg)
		plan := e.pendingEditPlan
		e.pendingEditPlan = nil // consume before execution

		// Restore task state from the plan snapshot, then mark confirmed.
		// Order matters: restore first, then set PlanConfirmed/ConfirmedScope
		// so they aren't overwritten by the restore.
		if plan.State != nil {
			*e.state = *plan.State
		}
		e.state.PlanConfirmed = true
		e.state.ConfirmedScope = true // prevents guard re-blocking subsequent edits in this session

		msg := "✅ 修改方案已确认，开始执行..."
		if !zh {
			msg = "✅ Edit plan confirmed, executing..."
		}
		e.history = append(e.history, Message{Role: "user", Content: msg, Timestamp: time.Now()})

		// Re-emit the assistant message with tool_calls so that subsequent tool
		// result messages have a valid preceding assistant reference.
		// The API requires: assistant(tool_calls) → tool(tool_call_id) ordering.
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

		// Execute the stored calls directly, skipping non-destructive
		// read/grep/glob — their results were consumed by the model's
		// reasoning before the plan was blocked. Re-executing them on
		// confirmation would be redundant (user already agreed to the plan).
		regularCalls := make([]ToolCallRequest, 0, len(plan.Calls))
		for _, c := range plan.Calls {
			if c.Name == HandoffToolName {
				if e.config.OnProgress != nil {
					e.config.OnProgress(ProgressEvent{Type: "agent_start", Name: "handoff", Detail: summarizeArgs(c.Input)})
				}
				result := e.executeHandoff(ctx, c)
				if e.config.OnProgress != nil {
					e.config.OnProgress(ProgressEvent{Type: "agent_done", Name: "handoff", Detail: briefDigest(result.Digest)})
				}
				e.history = append(e.history, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()})
			} else if c.Name != "read" && c.Name != "grep" && c.Name != "glob" {
				regularCalls = append(regularCalls, c)
			}
		}
		if len(regularCalls) > 0 {
			for _, call := range regularCalls {
				if e.config.OnProgress != nil {
					e.config.OnProgress(ProgressEvent{Type: "tool_start", Name: call.Name, Detail: summarizeArgs(call.Input)})
				}
			}
			toolResults := e.tools.Execute(ToolExecContext{WorkDir: e.config.WorkDir, SessionID: e.config.SessionID, TurnNumber: e.state.TurnNumber}, regularCalls)
			for _, result := range toolResults {
				if e.config.OnProgress != nil {
					e.config.OnProgress(ProgressEvent{Type: "tool_done", Name: result.ToolName, Detail: briefDigest(result.Digest), FullDetail: result.Digest})
				}
				e.history = append(e.history, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()})
			}
			e.updateTaskStateFromTools(regularCalls, toolResults)
		}
		// Fall through to the agent loop below — the agent can see tool results
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

	// Skill activation confirmation — model called activate_skill, user responded
	if e.state.PendingActivateSkill != "" {
		skillName := e.state.PendingActivateSkill
		e.state.PendingActivateSkill = ""
		if isDangerousConfirmation(userMsg) {
			// User confirmed — activate the skill
			s := e.skills.Get(skillName)
			if s == nil {
				// Try case-insensitive match
				for _, sk := range e.skills.All() {
					if strings.EqualFold(sk.Name, skillName) {
						s = sk
						break
					}
				}
			}
			if s != nil {
				e.activatedSkills[s.Name] = true
				e.lastActivatedSkill = s.Name
				e.state.ActiveSkillName = s.Name
				e.state.ActiveSkillContent = s.Content
				skillMsg := fmt.Sprintf(
					"[SKILL ACTIVATED: %s]\n\nThe following methodology has been activated per user request. Follow it precisely.\n\n%s",
					s.Name, s.Content,
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
				msg := fmt.Sprintf("✅ Skill `%s` activated.", s.Name)
				if zh {
					msg = fmt.Sprintf("✅ 已激活 skill `%s`。", s.Name)
				}
				e.history = append(e.history, Message{Role: "user", Content: msg, Timestamp: time.Now()})
			} else {
				msg := fmt.Sprintf("Skill '%s' not found. Available skills: /skills", skillName)
				if zh {
					msg = fmt.Sprintf("技能 '%s' 不存在。可用技能: /skills", skillName)
				}
				e.history = append(e.history, Message{Role: "user", Content: msg, Timestamp: time.Now()})
			}
		} else {
			// User said something else — skill activation declined
			msg := fmt.Sprintf("Skill activation '%s' declined by user.", skillName)
			if zh {
				msg = fmt.Sprintf("已取消激活 skill `%s`。", skillName)
			}
			e.history = append(e.history, Message{Role: "user", Content: msg, Timestamp: time.Now()})
		}
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

	// Roundtable Explore → extract proposals from the main agent's output
	// and transition to Review phase (awaiting user "都评一下" command).
	if e.state.Roundtable != nil && e.state.Roundtable.Phase == RoundtableExplore {
		summary := ""
		for i := len(e.history) - 1; i >= 0; i-- {
			if e.history[i].Role == "assistant" && e.history[i].Content != "" {
				summary = e.history[i].Content
				break
			}
		}
		summary = stripDSMLTokens(summary)
		proposals := extractProposals(summary)
		if len(proposals) == 0 && summary != "" {
			proposals = []string{summary}
		}
		if len(proposals) > 0 {
			e.state.Roundtable.Proposals = proposals
			e.state.Roundtable.Phase = RoundtableReview
			// Append the review prompt to the response
			if zh {
				summary = summary + "\n\n💡 输入「都评一下」让所有角色同时评审这些方案，或指定某个方案（如「评方案2」）单独评审。"
			} else {
				summary = summary + "\n\n💡 Say \"review all\" to have all roles review these proposals, or specify one (e.g. \"review approach 2\")."
			}
			return &EngineResponse{Summary: summary, Stage: StageAct}, nil
		}
	}

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

	summary := ""
	for i := len(e.history) - 1; i >= 0; i-- {
		if e.history[i].Role == "assistant" && e.history[i].Content != "" {
			summary = e.history[i].Content
			break
		}
	}
	summary = stripDSMLTokens(summary)
	if summary == "" {
		summary = "Done"
		if zh {
			summary = "完成"
		}
	}
	return &EngineResponse{Summary: summary, Stage: StageVerifyCompact}, nil
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
	return false
}

func isSingleConfirmWord(word string) bool {
	switch word {
	case "yes", "y", "ok", "okay", "confirm", "proceed", "go", "do", "it", "sure", "yep",
		"同意", "确认", "是", "执行", "可以", "好的", "好", "行",
		"对", "对的", "没问题", "嗯", "开始", "改", "改吧", "做", "做吧", "来", "来吧", "干", "干吧", "去吧", "吧":
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

// deactivateSkill clears the active skill state, releasing the agent from
// the skill's methodology constraints.
func (e *Engine) deactivateSkill() {
	e.state.ActiveSkillName = ""
	e.state.ActiveSkillContent = ""
	e.matchedSkillsContent = ""
	// Keep lastActivatedSkill for chain tracking purposes
	// Keep activatedSkills map for deduplication purposes
	// Reset TDD-specific phase tracking
	e.tddPhase = ""
	e.tddPhaseDetail = ""
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
