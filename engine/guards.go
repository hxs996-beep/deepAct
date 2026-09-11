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
	loop  *LoopTracker
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
	autoConfirm        bool
	dangerousPending   string // normalized command pending user confirmation
	dangerousConfirmed map[string]bool
	isChinese          bool // session-locked language flag for bilingual prompts
}

func NewScopeGuard(autoConfirm bool) *ScopeGuard {
	return &ScopeGuard{
		autoConfirm:        autoConfirm,
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

func (g *ScopeGuard) CheckTool(call ToolCallRequest, state *TaskState) GuardAction {
	// Layer 1: Always check bash commands for dangerous patterns, regardless of autoConfirm
	if call.Name == "bash" {
		if action := checkDangerousBash(call.Input, g); action.Type != GuardAllow {
			return action
		}
	}

	// Layer 2: Scope confirmation check (respects autoConfirm)
	if g.autoConfirm || state == nil || state.ConfirmedScope {
		return GuardAction{Type: GuardAllow}
	}

	if isDestructiveTool(call.Name) {
		return GuardAction{
			Type:    GuardAskUser,
			Message: "Scope not confirmed for this operation / 操作范围未确认",
		}
	}
	return GuardAction{Type: GuardAllow}
}

// dangerousPattern describes a command pattern and why it's dangerous.
type dangerousPattern struct {
	pattern string
	reason  string
}

// systemLevelPatterns are patterns so destructive they are ALWAYS hard-blocked,
// even with user confirmation. These can destroy the OS or hardware.
var systemLevelPatterns = []dangerousPattern{
	{"rm -rf / --no-preserve-root", "irreversible system-wide delete"},
	{"rm -rf /* ", "irreversible system-wide delete"},
	{":(){ :|:", "fork bomb — system crash"},
	{":() { :|:& };:", "fork bomb — system crash"},
	{"dd if=/dev/sd", "raw disk write — data destruction"},
	{"dd if=/dev/", "raw disk write — data destruction"},
	{"mkfs.ext", "filesystem creation — data loss"},
	{"mkfs.xfs", "filesystem creation — data loss"},
	{"mkfs.btrfs", "filesystem creation — data loss"},
	{"> /dev/sd", "raw disk write — data destruction"},
}

// projectLevelPatterns are dangerous operations that may be legitimate
// with user confirmation. The guard asks the user before allowing these.
var projectLevelPatterns = []dangerousPattern{
	{"rm -rf", "recursive force delete — irreversible data loss"},
	{"rm -fr", "recursive force delete — irreversible data loss"},
	{"rm --recursive", "recursive delete — data loss"},
	{"rm *", "bulk delete all files"},
	{"sudo rm", "privileged delete — bypasses file permissions"},
	{"sudo dd", "privileged raw disk write"},
	{"sudo chmod", "privileged permission change"},
	{"sudo mount", "privileged filesystem mount"},
	{"curl | sh", "pipe remote script to shell — arbitrary code execution"},
	{"curl | bash", "pipe remote script to shell — arbitrary code execution"},
	{"wget | sh", "pipe remote script to shell — arbitrary code execution"},
	{"wget | bash", "pipe remote script to shell — arbitrary code execution"},
	{"chmod 777 /", "world-writable root directory"},
	{"chmod -r 777", "world-writable recursive permission change"},
	{"> /etc/", "overwrite system configuration file"},
	{"/dev/tcp/", "network redirect — data exfiltration risk"},
	{"crontab", "scheduled task modification — persistence risk"},
	{"git push --force", "force push — overwrites remote history"},
	{"git push -f", "force push — overwrites remote history"},
	{"git reset --hard", "destructive git reset — loss of local changes"},
	{"git branch -d", "delete git branch"},
	{"git branch -D", "force delete git branch"},
	{"shred", "secure file deletion — irreversible"},
	{"truncate -s 0", "zero-out file content — data loss"},
	{":>", "truncate file — data loss"},
	{"drop table", "SQL table deletion — database data loss"},
	{"drop database", "SQL database deletion — database data loss"},
}

// checkDangerousBash inspects a bash command for dangerous patterns.
// Returns GuardBlock for system-level threats (hard stop),
// GuardAskUser for project-level threats (user can confirm),
// GuardAllow if safe.
func checkDangerousBash(input json.RawMessage, g *ScopeGuard) GuardAction {
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return GuardAction{Type: GuardAllow}
	}
	cmd, ok := m["command"].(string)
	if !ok || cmd == "" {
		return GuardAction{Type: GuardAllow}
	}

	// Normalize: collapse whitespace, lowercase — prevents trivial bypasses like "rm  -rf"
	normalized := strings.Join(strings.Fields(cmd), " ")
	lowered := strings.ToLower(normalized)

	// Layer 1: System-level patterns — always hard-block
	for _, dp := range systemLevelPatterns {
		if strings.Contains(lowered, dp.pattern) {
			return GuardAction{
				Type:    GuardBlock,
				Message: fmt.Sprintf("✗ System-level dangerous command blocked (irreversible): %s\nFull command: %s", dp.reason, cmd),
			}
		}
	}

	// Layer 2: Project-level patterns — ask user, unless already confirmed
	for _, dp := range projectLevelPatterns {
		if strings.Contains(lowered, dp.pattern) {
			// Check if this exact command was already confirmed by user
			if g.dangerousConfirmed[normalized] {
				continue // skip this pattern — user already approved
			}

			g.dangerousPending = normalized
			return GuardAction{
				Type: GuardAskUser,
				Message: pickPrompt(g.isChinese,
					fmt.Sprintf("⚠ Dangerous command: %s\n> `%s`\n\n[Y] confirm  [N] cancel, or type an alternative suggestion for the AI to reconsider", dp.reason, cmd),
					fmt.Sprintf("⚠ 危险命令: %s\n> `%s`\n\n[Y] 确认执行  [N] 取消，或输入其他建议让 AI 重新处理", dp.reason, cmd),
				),
			}
		}
	}

	return GuardAction{Type: GuardAllow}
}

func isDestructiveTool(name string) bool {
	switch name {
	case "edit", "write", "bash":
		return true
	default:
		return false
	}
}
