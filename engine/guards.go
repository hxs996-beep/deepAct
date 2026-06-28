package engine

import (
	"crypto/sha256"
	"encoding/hex"
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

// LoopGuard detects when the agent repeats the same operation on the same
// content without making progress. It tracks (toolName, path, contentHash)
// tuples and blocks when the same tuple appears too many times in a session.
// Content hash distinguishes different edits on the same file (e.g., different
// old_string→new_string pairs for the edit tool, or different file content for
// write), avoiding false positives when modifying multiple locations.
type LoopGuard struct {
	mu         sync.Mutex
	entries    map[string]*loopEntry // key: "toolName:path:contentHash"
	maxRepeats int
}

type loopEntry struct {
	count int
}

func NewLoopGuard(maxRepeats int) *LoopGuard {
	if maxRepeats <= 0 {
		maxRepeats = 4
	}
	return &LoopGuard{
		entries:    make(map[string]*loopEntry),
		maxRepeats: maxRepeats,
	}
}

// extractToolKey extracts a unique key from a tool call for loop detection.
// Returns "toolName:path:contentHash" for destructive tools, or "" for
// exploratory tools. Content hash ensures that different edits on the same
// file (different old_string→new_string) are treated as distinct operations,
// preventing false loop detection when modifying multiple locations.
func extractToolKey(call ToolCallRequest) string {
	path := extractPathField(call.Input)
	if path == "" {
		return ""
	}

	var contentHash string
	switch call.Name {
	case "edit":
		contentHash = extractEditContentHash(call.Input)
	case "write":
		contentHash = extractWriteContentHash(call.Input)
	case "read":
		// Track read by path + scope (symbol/offset/limit) — different sections
		// of the same file are distinct operations; only truly repeated reads block.
		scopeHash := extractReadScopeHash(call.Input)
		if scopeHash == "" {
			return "read:" + path
		}
		return "read:" + path + ":" + scopeHash
	default:
		// grep/glob/bash etc. — not tracked for loops
		return ""
	}

	if contentHash == "" {
		return ""
	}

	return call.Name + ":" + path + ":" + contentHash
}

// extractEditContentHash computes sha256("old_string→new_string") from edit input.
func extractEditContentHash(input json.RawMessage) string {
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	oldStr, _ := m["old_string"].(string)
	newStr, _ := m["new_string"].(string)
	h := sha256.Sum256([]byte(oldStr + "\x00" + newStr))
	return hex.EncodeToString(h[:])
}

// extractWriteContentHash computes sha256(content) from write input.
func extractWriteContentHash(input json.RawMessage) string {
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	content, _ := m["content"].(string)
	if content == "" {
		return ""
	}
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// extractReadScopeHash computes a hash of (symbol, offset, limit) from read input.
// Different reads on the same file (different sections) produce different hashes,
// preventing false loop detection when exploring code. Returns "" if no scope params.
func extractReadScopeHash(input json.RawMessage) string {
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	symbol, _ := m["symbol"].(string)
	offset, _ := m["offset"].(float64)
	limit, _ := m["limit"].(float64)

	if symbol == "" && offset == 0 && limit == 0 {
		return "" // bare read with just path, fall back to path-only tracking
	}

	h := sha256.Sum256([]byte(fmt.Sprintf("symbol=%s|offset=%d|limit=%d", symbol, int(offset), int(limit))))
	return hex.EncodeToString(h[:])
}

// readMultiTargetView is engine's view of a read_multi target (mirrors the
// tools/builtin readMultiTarget struct, kept unexported and local to avoid a
// tools→engine import).
type readMultiTargetView struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// parseReadMultiTargets parses the targets array from a read_multi tool call's
// input. Returns nil on error.
func parseReadMultiTargets(input json.RawMessage) []readMultiTargetView {
	var m struct {
		Targets []readMultiTargetView `json:"targets"`
	}
	if err := json.Unmarshal(input, &m); err != nil {
		return nil
	}
	return m.Targets
}

// Check inspects a tool call for loop behavior. Returns GuardBlock if the
// same (tool, path, contentHash) tuple has been repeated too many times.
// Reset clears all loop tracking state (e.g., on new user message).
func (g *LoopGuard) Reset() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.entries = make(map[string]*loopEntry)
}

func extractPathField(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	if p, ok := m["path"].(string); ok {
		return p
	}
	if p, ok := m["file_path"].(string); ok {
		return p
	}
	return ""
}

func (g *LoopGuard) Check(call ToolCallRequest) GuardAction {
	if g == nil {
		return GuardAction{Type: GuardAllow}
	}

	k := extractToolKey(call)
	if k == "" {
		return GuardAction{Type: GuardAllow}
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	entry, exists := g.entries[k]
	if !exists {
		entry = &loopEntry{}
		g.entries[k] = entry
	}

	entry.count++
	if entry.count >= g.maxRepeats {
		return GuardAction{
			Type: GuardBlock,
			Message: fmt.Sprintf(
				"Loop detected: %s %q repeated %d times. The agent appears to be repeating the same operation without making progress.",
				call.Name, k, entry.count,
			),
		}
	}

	return GuardAction{Type: GuardAllow}
}

type GuardSystem struct {
	scope *ScopeGuard
	loop  *LoopGuard
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
				Message: fmt.Sprintf("❌ System-level dangerous command blocked (irreversible): %s\nFull command: %s", dp.reason, cmd),
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
				Type:    GuardAskUser,
				Message: pickPrompt(g.isChinese,
					fmt.Sprintf("⚠️ Dangerous command: %s\n> `%s`\n\n[Y] confirm  [N] cancel, or type an alternative suggestion for the AI to reconsider", dp.reason, cmd),
					fmt.Sprintf("⚠️ 危险命令: %s\n> `%s`\n\n[Y] 确认执行  [N] 取消，或输入其他建议让 AI 重新处理", dp.reason, cmd),
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
