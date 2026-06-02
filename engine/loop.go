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
	Router     ModelRouter
	Agents     *AgentRegistry
	Skills     *skill.Registry
}

type Engine struct {
	model      ModelClient
	tools      ToolExecutor
	policy     PolicyChecker
	context    ContextBuilder
	compressor Compressor
	session    SessionStore
	router     ModelRouter
	agents     *AgentRegistry
	skills     *skill.Registry
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
		router:     deps.Router,
		agents:     deps.Agents,
		skills:     deps.Skills,
		config:     cfg,
		state:      &TaskState{TaskID: cfg.SessionID},
		history:    make([]Message, 0),
		guards:     guard,
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

	// Reset loop guard at the start of each user message
	if e.guards.loop != nil {
		e.guards.loop.Reset()
	}

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
		// The previous attempt was blocked with GuardAskUser; now the user confirmed.
		// The agent must re-issue the exact same tool call since the blocked one
		// was closed with a tool message containing "Blocked: ...".
		reissueHint := fmt.Sprintf("用户已确认执行危险命令。请重新执行之前被阻断的命令: `%s`", confirmedCmd)
		if !zh {
			reissueHint = fmt.Sprintf("The user confirmed the dangerous command. Please re-issue the previously blocked command: `%s`", confirmedCmd)
		}
		e.history = append(e.history, Message{Role: "user", Content: reissueHint, Timestamp: time.Now()})
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

	// Match skills against user message and inject matching skill content.
	// Skills are methodology templates (debugging, brainstorming, verification)
	// that shape the agent's approach to the current task.
	// IMPORTANT: Skills are appended as pinned messages at the END of the messages
	// array (not mixed into e.history) to preserve the stable prefix cache.
	if e.skills != nil {
		matched := e.skills.Match(userMsg)
		if len(matched) > 0 {
			var skillTexts []string
			for _, s := range matched {
				skillTexts = append(skillTexts, s.Content)
			}
			skillMsg := fmt.Sprintf(
				"[SKILLS — matched: %s]\n\nThe following methodology templates have been activated for this task. Follow them precisely.\n\n%s",
				joinSkillNames(matched),
				strings.Join(skillTexts, "\n\n---\n\n"),
			)
			e.pendingPinnedMessages = append(e.pendingPinnedMessages, skillMsg)
			if e.config.OnProgress != nil {
				for _, s := range matched {
					e.config.OnProgress(ProgressEvent{
						Type:   "skill_activated",
						Name:   s.Name,
						Detail: s.Description,
					})
				}
			}
		}
	}

	// Scope is implicitly confirmed when user sends any message
	if !e.state.ConfirmedScope {
		e.state.ConfirmedScope = true
	}

	turns := 0
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
	switch strings.ToLower(strings.TrimSpace(msg)) {
	case "yes", "y", "ok", "okay", "confirm", "proceed", "同意", "确认", "是", "执行", "可以", "好的", "好", "行":
		return true
	default:
		return false
	}
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
