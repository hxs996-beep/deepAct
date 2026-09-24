package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

type GuardAction struct {
	Type    string
	Message string
}

const (
	GuardAllow    = "allow"
	GuardBlock    = "block"
	GuardDiagnose = "diagnose"
	GuardAskUser  = "ask_user"
)

// LoopTracker is the unified counting core for all loop guards. Four
// configured instances replace LoopGuard / ReadLoopState / ErrorLoopState /
// ProgressLoopState: they differ only in key granularity, thresholds, and
// whether a success resets the streak. A new loop variant = a new instance +
// key construction, never a new struct.
type LoopTracker struct {
	mu             sync.Mutex
	counts         map[string]int
	nudgeAt        int // 0 = no nudge tier
	blockAt        int
	resetOnSuccess bool // error/progress semantics: a success clears the key
}

// NewLoopTracker creates a LoopTracker. blockAt<=0 defaults to 4; a nudgeAt
// >= blockAt is treated as no nudge tier.
func NewLoopTracker(nudgeAt, blockAt int, resetOnSuccess bool) *LoopTracker {
	if blockAt <= 0 {
		blockAt = 4
	}
	if nudgeAt >= blockAt {
		nudgeAt = 0
	}
	return &LoopTracker{
		counts:         make(map[string]int),
		nudgeAt:        nudgeAt,
		blockAt:        blockAt,
		resetOnSuccess: resetOnSuccess,
	}
}

// Check records a key occurrence and returns GuardAllow, GuardDiagnose
// (nudge, when count == nudgeAt), or GuardBlock (when count >= blockAt).
// For progress/error instances, success=true clears the streak for key
// (progress uses key="" as a global single counter).
func (t *LoopTracker) Check(key string, success bool) GuardAction {
	if t == nil {
		return GuardAction{Type: GuardAllow}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if success && t.resetOnSuccess {
		delete(t.counts, key)
		return GuardAction{Type: GuardAllow}
	}
	t.counts[key]++
	switch {
	case t.nudgeAt > 0 && t.counts[key] == t.nudgeAt:
		return GuardAction{Type: GuardDiagnose, Message: "loop-nudge"}
	case t.counts[key] >= t.blockAt:
		return GuardAction{Type: GuardBlock, Message: "loop-block"}
	default:
		return GuardAction{Type: GuardAllow}
	}
}

// Reset clears all tracking state (e.g., on new user message / new Run).
func (t *LoopTracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts = make(map[string]int)
}

type GuardSystem struct {
	scope *ScopeGuard
}

// SetLanguage propagates the session-locked language flag to the scope guard
// for bilingual dangerous-command prompts.
func (g *GuardSystem) SetLanguage(zh bool) {
	if g == nil || g.scope == nil {
		return
	}
	g.scope.SetLanguage(zh)
}

type ScopeGuard struct {
	dangerousPending   string // normalized command pending user confirmation
	dangerousConfirmed map[string]bool
	isChinese          bool // session-locked language flag for bilingual prompts
}

func NewScopeGuard() *ScopeGuard {
	return &ScopeGuard{
		dangerousConfirmed: make(map[string]bool),
	}
}

// SetLanguage sets the session-locked language flag used for bilingual guard messages.
func (g *ScopeGuard) SetLanguage(zh bool) {
	g.isChinese = zh
}

// ConfirmDangerous marks a pending dangerous command as confirmed by the user.
func (g *ScopeGuard) ConfirmDangerous(normalizedCmd string) {
	if normalizedCmd != "" {
		g.dangerousConfirmed[normalizedCmd] = true
	}
	g.dangerousPending = ""
}

// DangerousPending returns the pending dangerous command, if any.
func (g *ScopeGuard) DangerousPending() string {
	return g.dangerousPending
}

// CheckTool inspects a bash tool call for dangerous patterns that require
// explicit user confirmation before execution.
func (g *ScopeGuard) CheckTool(call ToolCallRequest) GuardAction {
	if call.Name == "bash" {
		if action := checkDangerousBash(call.Input, g); action.Type != GuardAllow {
			return action
		}
	}
	return GuardAction{Type: GuardAllow}
}

// checkDangerousBash judges a bash command structurally (see judgeDanger) and
// maps the verdict onto a guard action: GuardBlock for irreversible system-level
// threats, GuardAskUser for project-level threats the user can confirm,
// GuardAllow otherwise.
func checkDangerousBash(input json.RawMessage, g *ScopeGuard) GuardAction {
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return GuardAction{Type: GuardAllow}
	}
	cmd, ok := m["command"].(string)
	if !ok || cmd == "" {
		return GuardAction{Type: GuardAllow}
	}

	verdict := judgeDanger(cmd)
	switch verdict.kind {
	case dangerSystem:
		return GuardAction{
			Type:    GuardBlock,
			Message: fmt.Sprintf("✗ System-level dangerous command blocked (irreversible): %s\nFull command: %s", verdict.reason, cmd),
		}
	case dangerProject:
		// Whitespace-collapsed form is the confirmation key, so re-issuing the
		// same command with different spacing stays confirmed.
		key := strings.Join(strings.Fields(cmd), " ")
		if g.dangerousConfirmed[key] {
			return GuardAction{Type: GuardAllow}
		}
		g.dangerousPending = key
		return GuardAction{
			Type: GuardAskUser,
			Message: pickPrompt(g.isChinese,
				fmt.Sprintf("⚠ Dangerous command: %s\n> `%s`\n\n[Y] confirm  [N] cancel, or type an alternative suggestion for the AI to reconsider", verdict.reason, cmd),
				fmt.Sprintf("⚠ 危险命令: %s\n> `%s`\n\n[Y] 确认执行  [N] 取消，或输入其他建议让 AI 重新处理", verdict.reason, cmd),
			),
		}
	}
	return GuardAction{Type: GuardAllow}
}
