package engine

import (
	"encoding/json"
	"fmt"
)

type GuardAction struct {
	Type    string
	Message string
}

const (
	GuardAllow   = "allow"
	GuardBlock   = "block"
	GuardAskUser = "ask_user"
)

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
