package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/deepact/deepact/skill"
	dlog "github.com/deepact/deepact/internal/log"
)

var loopLog = dlog.New("[loop] ")

type EngineDeps struct {
	Model      ModelClient
	Tools      ToolExecutor
	Policy     PolicyChecker
	Context    ContextBuilder
	Compressor Compressor
	Session    SessionStore
	Agents     *AgentRegistry
	Skills     *skill.Registry
	Router     ModelRouter
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
	hall       *ConferenceHall
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

	// pendingEditPlan holds the agent's proposed edits for user confirmation.
	// When non-nil, the agent has proposed file modifications and is awaiting
	// user approval before execution.
	pendingEditPlan *PendingEditPlan

	// lastMarkerCount tracks how many MemoryMarkers have been written to
	// AccumulatedBlocks. On each turn, accumulateTurnBlock only appends
	// newly-added markers (state.MemoryMarkers[lastMarkerCount:]) to avoid
	// the same finding being repeated 20+ times in AccumulatedBlocks.
	lastMarkerCount int
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
		activatedSkills: make(map[string]bool),
	}
	e.hall = NewConferenceHall(e)

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
	e.matchedSkillsContent = ""

	// Conference command parsing — must run BEFORE conference init
	// so slash commands take priority over automatic classification.
	cmd := parseConferenceCommand(userMsg)
	if cmd != nil {
		switch cmd.Phase {
		case PhasePlanning:
			if cmd.Goal == "" {
				msg := "Please provide a goal. Usage: /plan <goal>"
				return &EngineResponse{Summary: msg, Stage: StageAct}, nil
			}
			e.state.PlanConfirmed = false
			e.state.Conference = &ConferenceState{
				Enabled: true,
				Phase:   PhasePlanning,
				Board:   ConferenceBoard{Goal: cmd.Goal, Phase: PhasePlanning},
			}
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{
					Type:   "conference_enter",
					Name:   "planning",
					Detail: "进入多智能体会议 — 规划阶段",
				})
			}
			// Fall through to Advance() below

		case PhaseExecute:
			if cmd.Goal == "" {
				msg := "Please provide a goal. Usage: /implement <goal>"
				return &EngineResponse{Summary: msg, Stage: StageAct}, nil
			}
			e.state.PlanConfirmed = false
			// Save parent context before overwriting — preserves the original multi-defect
			// goal/plan so it can be restored when the sub-task completes.
			if e.state.Conference != nil && e.state.Conference.Enabled && e.state.Conference.Board.Plan != "" {
				e.state.ParentContext = &ParentBoard{
					Goal: e.state.Conference.Board.Goal,
					Plan: e.state.Conference.Board.Plan,
				}
			}
			e.state.Conference = &ConferenceState{
				Enabled: true,
				Phase:   PhaseExecute,
				Board:   ConferenceBoard{Goal: cmd.Goal, Phase: PhaseExecute},
			}
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{
					Type:   "conference_enter",
					Name:   "implementation",
					Detail: "Starting implementation...",
				})
			}
			e.state.ConfirmedScope = true
			// Fall through to normal agent loop below

		case PhaseReview:
			// /审查 — force a fresh review against the current plan
			if e.state.Conference != nil && e.state.Conference.Enabled {
				e.state.Conference.Phase = PhaseReview
				e.state.Conference.Board.PendingReview = false // force fresh review
				// Fall through to Advance()
			} else {
				// No conference — run standalone
				resp, err := e.hall.runStandalonePhase(ctx, cmd)
				if err != nil {
					return nil, fmt.Errorf("standalone review: %w", err)
				}
				return resp, nil
			}

		default:
			// Unknown phase — run standalone
			resp, err := e.hall.runStandalonePhase(ctx, cmd)
			if err != nil {
				return nil, fmt.Errorf("standalone phase: %w", err)
			}
			return resp, nil
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

	// PhaseDone reset — completed conferences are cleaned up.
	// New tasks start fresh; user uses /plan or /implement to re-enter.
	if e.state.Conference != nil && e.state.Conference.Enabled {
		if e.state.Conference.Phase == PhaseDone {
			// Save parent context before clearing
			if board := e.state.Conference.Board; board.Goal != "" && e.state.ParentContext == nil {
				e.state.ParentContext = &ParentBoard{Goal: board.Goal, Plan: board.Plan}
			}
			e.state.Conference = nil
		}
	}

	// Conference is only entered via explicit slash commands (/plan, /implement, /review).
	// No automatic intent classification — the user chooses the mode.
	inConference := e.state.Conference != nil && e.state.Conference.Enabled
	e.updateGoalFromFirstMessage(userMsg)

	if e.pendingEditPlan != nil {
		if !isDangerousConfirmation(userMsg) {
			e.pendingEditPlan = nil
			e.state.PlanConfirmed = false
		}
	}

	if inConference {
		phase := e.state.Conference.Phase
		if phase == PhasePlanning || phase == PhaseReview {
			confResp, err := e.hall.Advance(ctx, userMsg)
			if err != nil {
				return nil, fmt.Errorf("conference: %w", err)
			}
			if confResp != nil {
				return confResp, nil
			}
		}
		// PhaseExecute falls through to normal agent loop below
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

	// Inject conference context for execute phase
	var didExecute bool
	if e.state.Conference != nil && e.state.Conference.Enabled && e.state.Conference.Phase == PhaseExecute && !e.state.Conference.Board.PendingReview {
		board := e.state.Conference.Board
		planMsg := fmt.Sprintf("## Implementation Plan\n\n%s\n\nPlease implement this according to the plan.", board.Plan)
		if board.Goal != "" {
			planMsg += "\n\n## Goal\n" + board.Goal
		}
		e.history = append(e.history, Message{Role: "user", Content: planMsg, Timestamp: time.Now()})
		didExecute = true
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
			return nil, err
		}
		if turnResult.Blocked {
			return &EngineResponse{Questions: turnResult.Questions, Stage: StageAct, Blocked: true, BlockedBy: turnResult.BlockedBy, FinishReason: turnResult.FinishReason}, nil
		}
		if turnResult.Done {
			break
		}

		// Detect loops: same tool+path+content repeated 5+ times.
		// Content signature is derived from content-bearing fields (old_string,
		// pattern, content, etc.), so different edits on the same file or different
		// reads of the same file are treated as distinct operations.
		if turnResult.LastOp != "" {
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
		turns++
	}
	// Advance session turn counter past the last executed turn so the next
	// Run() call continues from the correct position. +1 because 'turns' was
	// not incremented after a Done break — it still points to the completed turn.
	e.state.TurnNumber = turns + 1

	if err := e.emitEvent("act_complete", StageAct, nil); err != nil {
		return nil, err
	}

	// After implementation phase in conference mode, run challenger review
	if didExecute {
		e.state.Conference.Phase = PhaseReview
		confResp, err := e.hall.Advance(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("conference review: %w", err)
		}
		if confResp != nil {
			return confResp, nil
		}
	}

	if err := e.verifyAndCompact(); err != nil {
		return nil, err
	}

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
