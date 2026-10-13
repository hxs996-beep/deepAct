package engine

import (
	"encoding/json"
	"fmt"
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

// SetLanguage is gone: guard messages are English-only.

type ScopeGuard struct {
	dangerousPending   string // normalized command pending user confirmation
	dangerousConfirmed map[string]bool

	// Approval policy (see engine/approval.go). Zero values reproduce the
	// historical behaviour: no rules, nothing remembered, ask on project-level
	// danger.
	approvalMode  string
	approvalRules []ApprovalRule
	onLearned     func(command string)
}

func NewScopeGuard() *ScopeGuard {
	return &ScopeGuard{
		dangerousConfirmed: make(map[string]bool),
	}
}

// SetLanguage is gone: guard messages are English-only.

// ConfirmDangerous marks a pending dangerous command as confirmed by the user
// and reports it to the host so the confirmation outlives this process. The
// approval is recorded verbatim — nothing about it is widened automatically;
// broadening is a deliberate act expressed as a declared rule.
func (g *ScopeGuard) ConfirmDangerous(normalizedCmd string) {
	if g.dangerousConfirmed == nil {
		g.dangerousConfirmed = make(map[string]bool)
	}
	key := normalizeApprovalCommand(normalizedCmd)
	if key != "" {
		g.dangerousConfirmed[key] = true
	}
	g.dangerousPending = ""
	if g.onLearned != nil && key != "" {
		g.onLearned(key)
	}
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

// checkDangerousBash maps a bash command onto a guard action through a fixed
// chain of judgement layers, most authoritative first:
//
//  1. judgeDanger's dangerSystem — irreversible, hard-blocked, and never
//     overridable by user rules or remembered approvals.
//  2. Declared rules with decision=forbidden — hard-blocked, with the rule's
//     own reason surfaced.
//  3. Declared rules with decision=allow, and commands the user approved
//     earlier — pass without asking.
//  4. judgeDanger's dangerProject, and declared rules with decision=prompt —
//     ask the user, unless the approval mode is "never".
//
// The order is the safety contract: rules can silence a prompt, but they can
// never unlock layer 1. (The bash tool's own blocklist is a fifth, lowest-reach
// backstop that runs at execution time; see tools/builtin/bash.go.)
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
	if verdict.kind == dangerSystem {
		return GuardAction{
			Type:    GuardBlock,
			Message: fmt.Sprintf("✗ System-level dangerous command blocked (irreversible): %s\nFull command: %s", verdict.reason, cmd),
		}
	}

	decision, ruleReason := g.decideApproval(cmd)
	switch decision {
	case ApprovalForbidden:
		return GuardAction{Type: GuardBlock, Message: approvalBlockMessage(ruleReason, cmd)}
	case ApprovalAllow:
		return GuardAction{Type: GuardAllow}
	}

	// Layer 4: something wants confirmation. A rule prompt asks on its own
	// (that is the point of declaring it); otherwise only project-level danger
	// does.
	reason := ""
	switch {
	case decision == ApprovalPrompt:
		reason = ruleReason
		if reason == "" {
			reason = "matched an approval rule"
		}
	case verdict.kind == dangerProject:
		reason = verdict.reason
	}
	if reason == "" {
		return GuardAction{Type: GuardAllow}
	}
	if g.approvalMode == ApprovalModeNever {
		return GuardAction{Type: GuardAllow}
	}

	// Whitespace-collapsed form is the confirmation key, so re-issuing the same
	// command with different spacing stays confirmed.
	key := normalizeApprovalCommand(cmd)
	g.dangerousPending = key
	return GuardAction{Type: GuardAskUser, Message: approvalAskMessage(reason, cmd)}
}
