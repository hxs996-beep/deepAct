// Package config loads project configuration from .deepact/config.toml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/deepact/deepact/engine"
)

// File mirrors the structure of .deepact/config.toml.
type File struct {
	Model   modelConfig   `toml:"model"`
	Context contextConfig `toml:"context"`
	// Agents holds user-defined sub-agent roles. Each key is a role name that
	// becomes selectable in the handoff tool's agent enum; user roles override
	// built-in roles with the same name. Mirrors codex's agent_roles toml.
	Agents map[string]agentRoleConfig `toml:"agents"`
	// Conference field removed — ConferenceEnabled was dead code (never read by engine).
	// Conference state is managed via TaskState.Conference in the engine package.
	Team   teamConfig   `toml:"team"`
	LSP    lspConfig    `toml:"lsp"`
	Search searchConfig `toml:"search"`
	UI     uiConfig     `toml:"ui"`
	// Approval holds the command-approval policy: when to interrupt the user,
	// and which command families the user has declared rules for.
	Approval approvalConfig `toml:"approval"`
}

// approvalConfig mirrors the [approval] section:
//
//	[approval]
//	mode = "on-request"        # or "never"
//
//	[[approval.rules]]
//	program = "terraform"
//	args = ["destroy"]         # token prefix; "*" matches exactly one token
//	decision = "forbidden"     # allow | prompt | forbidden
//	reason = "生产环境禁地"
type approvalConfig struct {
	Mode  string               `toml:"mode"`
	Rules []approvalRuleConfig `toml:"rules"`
}

// approvalRuleConfig is one [[approval.rules]] entry.
type approvalRuleConfig struct {
	Program  string   `toml:"program"`
	Args     []string `toml:"args"`
	Decision string   `toml:"decision"`
	Reason   string   `toml:"reason"`
}

// agentRoleConfig is one user-defined sub-agent role (codex-style).
//
//	[agents.researcher]
//	description = "只读调研员"
//	persona     = "你是研究员……"
//	tools       = ["read", "grep", "glob", "lsp"]   # 或 ["*"] 全部工具
//	model       = "flash"                            # 可选，per-role 模型覆盖
//	structured_result = true                         # 可选，结构化完成
type agentRoleConfig struct {
	Description      string   `toml:"description"`
	Persona          string   `toml:"persona"`
	Tools            []string `toml:"tools"`
	Model            string   `toml:"model"`
	StructuredResult *bool    `toml:"structured_result"`
	// TextResult declares that this role's text-only reply IS its result (a
	// text-payload contract, e.g. ratd's JSON). Mutually exclusive with
	// structured_result: a structured role completes only via submit_result.
	TextResult *bool `toml:"text_result"`
}

// searchConfig configures the native web_search tool.
//
//	[search]
//	provider    = "tavily"
//	api_key     = "tvly-..."
//	base_url    = "https://api.tavily.com"
//	max_results = 5
type searchConfig struct {
	Provider   string `toml:"provider"`
	APIKey     string `toml:"api_key"`
	BaseURL    string `toml:"base_url"`
	MaxResults int    `toml:"max_results"`
}

// lspConfig holds per-language LSP server overrides:
//
//	[lsp.servers.python]
//	command = "pyright-langserver"
//	args    = ["--stdio"]
type lspConfig struct {
	Servers map[string]lspServerOverride `toml:"servers"`
}

// lspServerOverride overrides the auto-selected server for one language.
type lspServerOverride struct {
	Command  string   `toml:"command"`
	Args     []string `toml:"args,omitempty"`
	Language string   `toml:"language,omitempty"` // optional override of the LSP languageId
}

type modelConfig struct {
	Default    string `toml:"default"`
	Escalation string `toml:"escalation"`
	BaseURL    string `toml:"base_url"` // API base URL (e.g. https://api.deepseek.com). Defaults to DeepSeek official.
	APIKey     string `toml:"api_key"`  // DeepSeek/OpenRouter API key
	// MaxConcurrentRequests caps concurrent in-flight LLM requests (shared
	// AdaptiveLimiter slots). 0 = default 8. Lower it if the provider rate-limits
	// you (e.g. 1-minute TPM windows); raise it for parallel sub-agent workloads.
	MaxConcurrentRequests int `toml:"max_concurrent_requests"`
	// ReasoningEffort controls the DeepSeek thinking effort for all model calls
	// (main loop + sub-agents). Valid values: none | low | high | max (also
	// minimal/medium/xhigh/ultra accepted by the API and mapped). Empty = the
	// engine default (high, DeepSeek's own default).
	ReasoningEffort string `toml:"reasoning_effort"`
}

type contextConfig struct {
	MaxBudgetTokens int `toml:"max_budget_tokens"`
	// MaxOutputTokens caps the LLM completion length per turn (max_tokens).
	// 0 = use the engine default. DeepSeek's 1M context window supports large
	// completions; a generous budget lets the model emit full code edits in one
	// turn instead of being cut off and forced to continue piecemeal.
	MaxOutputTokens int `toml:"max_output_tokens"`
	// MaxOutstandingAsyncSubAgents caps dispatched-but-uncollected async
	// sub-agent jobs (a slot frees when agent_poll consumes a done result).
	// 0 = default 8.
	MaxOutstandingAsyncSubAgents int `toml:"max_outstanding_async_subagents"`
	// MaxSuspendedSubAgents caps how many suspended (awaiting_user) sub-agent
	// jobs are kept at once — each retains a whole history, so the table must
	// stay small. 0 = default 4; negative fails loud at startup.
	MaxSuspendedSubAgents int `toml:"max_suspended_subagents"`
	// MaxConcurrentSubAgents caps how many sub-agent runs may be in flight at
	// once across the whole delegation tree. 0 = default 6.
	MaxConcurrentSubAgents int `toml:"max_concurrent_subagents"`
	// MaxSubAgentDepth caps sub-agent nesting. 0 = default 2.
	MaxSubAgentDepth int `toml:"max_subagent_depth"`
}

// conferenceConfig struct removed — ConferenceEnabled was dead code (never read by engine).

// Load reads the TOML file at the given path. Returns nil if the file
// doesn't exist — callers should use Apply with their defaults.
//
// Decoding is STRICT: a key the struct does not know about is an error, not a
// silent no-op. A config key that stops working without saying so is the worst
// kind of failure — the user believes a guard is in place when it is not (this
// is how removed keys such as max_iterations / token_budget / max_turns would
// otherwise behave). Removed keys get a message that says what replaced them.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f File
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, err
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, unknownKeyError(path, undecoded)
	}
	return &f, nil
}

// removedConfigKeys maps keys that no longer exist to what took their place, so
// a stale config file fails loud with a fix instead of guessing.
var removedConfigKeys = map[string]string{
	"max_iterations":             "removed: a sub-agent run's convergence is its engine-computed token budget plus the 3-strike guards",
	"max_turns":                  "removed: the main agent has no turn cap",
	"token_budget":               "removed: no role can set or widen a run's budget",
	"sub_agent_token_budget":     "removed: the engine computes a run's budget from its context window (2×)",
	"run_sub_agent_token_budget": "removed: the delegation tree has no Run-level spend cap",
}

// unknownKeyError names every key the config file set that nothing reads.
func unknownKeyError(path string, keys []toml.Key) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: unknown configuration key(s) — nothing reads them, so they would silently do nothing:", path)
	for _, k := range keys {
		name := k.String()
		if why, ok := removedConfigKeys[k[len(k)-1]]; ok {
			fmt.Fprintf(&b, "\n  - %s (%s)", name, why)
			continue
		}
		fmt.Fprintf(&b, "\n  - %s (no such option; check the spelling)", name)
	}
	return errors.New(b.String())
}

// LoadProject reads .deepact/config.toml relative to the given work directory,
// then falls back to ~/.deepact/config.toml.
func LoadProject(workDir string) *File {
	// Project-level config takes priority
	if workDir != "" {
		f, err := Load(filepath.Join(workDir, ".deepact", "config.toml"))
		if err == nil && f != nil {
			return f
		}
	}
	// Fall back to user-level config
	home, err := os.UserHomeDir()
	if err == nil {
		f, err := Load(filepath.Join(home, ".deepact", "config.toml"))
		if err == nil && f != nil {
			return f
		}
	}
	return nil
}

// LoadAPIKey resolves the API key. The DEEPSEEK_API_KEY environment variable
// takes priority; otherwise the api_key field is read from .deepact/config.toml
// (project-level first, then user-level). Returns "" if not set.
func LoadAPIKey(workDir string) string {
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		return key
	}
	if f := LoadProject(workDir); f != nil {
		return f.Model.APIKey
	}
	return ""
}

// UserConfigPath returns the path to the user-level config file (~/.deepact/config.toml).
func UserConfigPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".deepact", "config.toml")
	}
	return filepath.Join(os.TempDir(), "deepact", "config.toml")
}

// SaveAPIKey writes the api_key field into the user-level config file
// (~/.deepact/config.toml), preserving any existing content. The file is
// created with restrictive permissions since it holds a secret.
func SaveAPIKey(key string) error {
	return saveAPIKeyAtPath(UserConfigPath(), key)
}

var (
	// existingAPIKeyLine matches an `api_key = ...` line (quoted or bare).
	existingAPIKeyLine = regexp.MustCompile(`(?m)^\s*api_key\s*=\s*.*$`)
	// modelSectionHeader matches a `[model]` table header on its own line.
	modelSectionHeader = regexp.MustCompile(`(?m)^(\s*\[model\]\s*)$`)
)

func saveAPIKeyAtPath(path, key string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config: %w", err)
	}
	content := string(data)
	quoted := fmt.Sprintf("%q", key)

	var updated string
	switch {
	case existingAPIKeyLine.MatchString(content):
		// Replace the existing api_key value in place.
		updated = existingAPIKeyLine.ReplaceAllString(content, "api_key = "+quoted)
	case modelSectionHeader.MatchString(content):
		// Insert api_key right after the [model] header.
		updated = modelSectionHeader.ReplaceAllString(content, "${1}\napi_key = "+quoted)
	default:
		// No [model] section yet — append one.
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		updated = content + "[model]\napi_key = " + quoted + "\n"
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// learnedApprovalsFile is the on-disk shape of ~/.deepact/approvals.toml.
type learnedApprovalsFile struct {
	Commands []string `toml:"commands"`
}

// LearnedApprovalsPath is where confirmed commands are remembered.
//
// Deliberately NOT config.toml: LoadProject returns either the project config or
// the user config (never a merge), so approvals stored in the user config would
// be shadowed — and silently stop working — in any project that ships its own
// .deepact/config.toml.
func LearnedApprovalsPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".deepact", "approvals.toml")
	}
	return filepath.Join(os.TempDir(), "deepact", "approvals.toml")
}

// LoadLearnedApprovals returns the commands the user confirmed in earlier
// sessions. A missing or unreadable file yields no approvals: this is a
// convenience store, never a startup blocker.
func LoadLearnedApprovals() []string {
	return loadLearnedApprovalsAt(LearnedApprovalsPath())
}

func loadLearnedApprovalsAt(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var parsed learnedApprovalsFile
	if err := toml.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	commands := make([]string, 0, len(parsed.Commands))
	for _, cmd := range parsed.Commands {
		if trimmed := strings.TrimSpace(cmd); trimmed != "" {
			commands = append(commands, trimmed)
		}
	}
	return commands
}

// AppendLearnedApproval records one confirmed command, de-duplicated, so
// re-confirming the same command does not grow the file without bound.
func AppendLearnedApproval(command string) error {
	return appendLearnedApprovalAt(LearnedApprovalsPath(), command)
}

func appendLearnedApprovalAt(path, command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}

	existing := loadLearnedApprovalsAt(path)
	for _, cmd := range existing {
		if cmd == command {
			return nil
		}
	}

	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(learnedApprovalsFile{Commands: append(existing, command)}); err != nil {
		return fmt.Errorf("encode approvals: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	// tmp+rename: a crash mid-write must not lose the approvals already learned.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write approvals: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("commit approvals: %w", err)
	}
	return nil
}

// applyApproval maps the [approval] section onto the engine config. An absent
// section leaves the engine defaults alone. Every malformed value fails loud:
// a typo here silently widening what runs without asking is exactly the failure
// mode this section exists to prevent.
func applyApproval(cfg *engine.EngineConfig, section approvalConfig) error {
	if mode := strings.TrimSpace(section.Mode); mode != "" {
		switch mode {
		case engine.ApprovalModeOnRequest, engine.ApprovalModeNever:
			cfg.ApprovalMode = mode
		default:
			return fmt.Errorf("[approval] mode must be %q or %q, got %q",
				engine.ApprovalModeOnRequest, engine.ApprovalModeNever, section.Mode)
		}
	}

	for i, rc := range section.Rules {
		program := strings.TrimSpace(rc.Program)
		if program == "" {
			return fmt.Errorf("[approval] rules[%d]: program is required", i)
		}
		decision := strings.TrimSpace(rc.Decision)
		switch decision {
		case engine.ApprovalAllow, engine.ApprovalPrompt, engine.ApprovalForbidden:
		default:
			return fmt.Errorf("[approval] rules[%d] (%s): decision must be %q, %q or %q, got %q",
				i, program, engine.ApprovalAllow, engine.ApprovalPrompt, engine.ApprovalForbidden, rc.Decision)
		}
		for _, arg := range rc.Args {
			if strings.TrimSpace(arg) == "" {
				return fmt.Errorf("[approval] rules[%d] (%s): args must not contain empty tokens", i, program)
			}
		}
		cfg.ApprovalRules = append(cfg.ApprovalRules, engine.ApprovalRule{
			Program:  program,
			Args:     rc.Args,
			Decision: decision,
			Reason:   strings.TrimSpace(rc.Reason),
		})
	}
	return nil
}

// Apply overwrites engine config fields that are explicitly set in the TOML file.
// Fields not present in the file keep their default values. It returns an
// error when an [agents] role names tools outside the sub-agent read-only
// universe, so an invalid role fails loud at startup instead of silently
// filtering at delegation time.
func Apply(cfg *engine.EngineConfig, f *File) error {
	if f == nil {
		return nil
	}
	if f.Model.Default != "" {
		cfg.FlashModelName = f.Model.Default
	}
	if f.Model.Escalation != "" {
		cfg.ModelName = f.Model.Escalation
	}
	// base_url is authoritative; if unset, the engine default (DeepSeek official)
	// is used. There is no provider→URL mapping — users point at any OpenAI-
	// compatible endpoint explicitly.
	if f.Model.BaseURL != "" {
		cfg.BaseURL = f.Model.BaseURL
	}
	if f.Model.MaxConcurrentRequests > 0 {
		cfg.MaxConcurrentRequests = f.Model.MaxConcurrentRequests
	}
	if f.Model.ReasoningEffort != "" {
		cfg.ReasoningEffort = f.Model.ReasoningEffort
	}
	if f.Context.MaxBudgetTokens > 0 {
		cfg.MaxContextTokens = f.Context.MaxBudgetTokens
	}
	if f.Context.MaxOutputTokens > 0 {
		cfg.MaxOutputTokens = f.Context.MaxOutputTokens
	}
	if f.Context.MaxOutstandingAsyncSubAgents > 0 {
		cfg.MaxOutstandingAsyncSubAgents = f.Context.MaxOutstandingAsyncSubAgents
	}
	if f.Context.MaxSuspendedSubAgents != 0 {
		if f.Context.MaxSuspendedSubAgents < 0 {
			return fmt.Errorf("[context] max_suspended_subagents must be 0 (default) or positive, got %d", f.Context.MaxSuspendedSubAgents)
		}
		cfg.MaxSuspendedSubAgents = f.Context.MaxSuspendedSubAgents
	}
	// Delegation-tree caps. 0 means "engine default"; a negative value is a
	// typo — fail loud rather than silently fall back.
	if f.Context.MaxConcurrentSubAgents != 0 {
		if f.Context.MaxConcurrentSubAgents < 1 {
			return fmt.Errorf("[context] max_concurrent_subagents must be positive, got %d", f.Context.MaxConcurrentSubAgents)
		}
		cfg.MaxConcurrentSubAgents = f.Context.MaxConcurrentSubAgents
	}
	if f.Context.MaxSubAgentDepth != 0 {
		if f.Context.MaxSubAgentDepth < 1 {
			return fmt.Errorf("[context] max_subagent_depth must be positive, got %d", f.Context.MaxSubAgentDepth)
		}
		cfg.MaxSubAgentDepth = f.Context.MaxSubAgentDepth
	}
	if err := applyApproval(cfg, f.Approval); err != nil {
		return err
	}
	// User-defined sub-agent roles: convert [agents] section to AgentSpecs.
	// tools = ["*"] means all sub-agent tools (empty ToolNames in AgentSpec
	// means the full read-only universe).
	for name, rc := range f.Agents {
		if strings.TrimSpace(name) == "" {
			continue
		}
		spec := engine.AgentSpec{
			ID:          engine.AgentID(name),
			Description: rc.Description,
			Persona:     rc.Persona,
			ModelName:   rc.Model,
		}
		tools := make([]string, 0, len(rc.Tools))
		for _, t := range rc.Tools {
			if t != "*" {
				tools = append(tools, t)
			}
		}
		// Universe check runs AFTER "*" stripping: ["*"] means the whole
		// read-only universe and must not be rejected here.
		if err := engine.ValidateSubAgentTools(tools); err != nil {
			return fmt.Errorf("[agents.%s] %w", name, err)
		}
		// Only set ToolNames when the user listed specific tools (empty = all).
		if len(tools) > 0 {
			spec.ToolNames = tools
		}
		if rc.StructuredResult != nil {
			spec.StructuredResult = *rc.StructuredResult
		}
		if rc.TextResult != nil {
			spec.TextResult = *rc.TextResult
		}
		// The two completion contracts are contradictory: structured means "only
		// submit_result counts", text means "the text reply is the result". Fail
		// loud rather than pick one silently.
		if spec.TextResult && spec.StructuredResult {
			return fmt.Errorf("[agents.%s] text_result and structured_result cannot both be true: a structured run completes only through submit_result", name)
		}
		cfg.AgentSpecs = append(cfg.AgentSpecs, spec)
	}
	// ConferenceEnabled was removed (dead code - Conference state is managed
	// via TaskState.Conference field in the engine, not via EngineConfig).
	return nil
}

type uiConfig struct {
	// Placeholder for future UI configuration (e.g., theme, font size).
}

type teamConfig struct {
	Members []string `toml:"members"` // member IDs to use (default: radical,defender,pragmatic,advocate)
}
