// Package config loads project configuration from .deepact/config.toml.
package config

import (
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
	Routing routingConfig `toml:"routing"`
	Context contextConfig `toml:"context"`
	Guards  guardsConfig  `toml:"guards"`
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
}

// agentRoleConfig is one user-defined sub-agent role (codex-style).
//
//	[agents.researcher]
//	description = "只读调研员"
//	persona     = "你是研究员……"
//	tools       = ["read", "grep", "glob", "lsp"]   # 或 ["*"] 全部工具
//	model       = "flash"                            # 可选，per-role 模型覆盖
//	max_iterations = 15                              # 可选，轮次上限
//	structured_result = true                         # 可选，结构化完成
type agentRoleConfig struct {
	Description      string   `toml:"description"`
	Persona          string   `toml:"persona"`
	Tools            []string `toml:"tools"`
	Model            string   `toml:"model"`
	MaxIterations    int      `toml:"max_iterations"`
	StructuredResult *bool    `toml:"structured_result"`
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
	Default     string `toml:"default"`
	Escalation  string `toml:"escalation"`
	BaseURL     string `toml:"base_url"` // API base URL (e.g. https://api.deepseek.com). Defaults to DeepSeek official.
	APIKey      string `toml:"api_key"`  // DeepSeek/OpenRouter API key
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

type routingConfig struct {
	RiskThreshold float64 `toml:"risk_threshold"`
}

type contextConfig struct {
	MaxBudgetTokens int `toml:"max_budget_tokens"`
	// MaxOutputTokens caps the LLM completion length per turn (max_tokens).
	// 0 = use the engine default. DeepSeek's 1M context window supports large
	// completions; a generous budget lets the model emit full code edits in one
	// turn instead of being cut off and forced to continue piecemeal.
	MaxOutputTokens int `toml:"max_output_tokens"`
}

type guardsConfig struct {
	ScopeGuard bool `toml:"scope_guard"`
}

// conferenceConfig struct removed — ConferenceEnabled was dead code (never read by engine).

// Load reads the TOML file at the given path. Returns nil if the file
// doesn't exist — callers should use Apply with their defaults.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f File
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return &f, nil
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

// Apply overwrites engine config fields that are explicitly set in the TOML file.
// Fields not present in the file keep their default values.
func Apply(cfg *engine.EngineConfig, f *File) {
	if f == nil {
		return
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
	if f.Routing.RiskThreshold > 0 {
		cfg.RiskThreshold = f.Routing.RiskThreshold
	}
	// User-defined sub-agent roles: convert [agents] section to AgentSpecs.
	// tools = ["*"] means all tools (empty ToolNames in AgentSpec means all).
	for name, rc := range f.Agents {
		if strings.TrimSpace(name) == "" {
			continue
		}
		spec := engine.AgentSpec{
			ID:          engine.AgentID(name),
			Description: rc.Description,
			Persona:     rc.Persona,
			ModelName:   rc.Model,
			MaxIterations: rc.MaxIterations,
		}
		tools := make([]string, 0, len(rc.Tools))
		for _, t := range rc.Tools {
			if t != "*" {
				tools = append(tools, t)
			}
		}
		// Only set ToolNames when the user listed specific tools (empty = all).
		if len(tools) > 0 {
			spec.ToolNames = tools
		}
		if rc.StructuredResult != nil {
			spec.StructuredResult = *rc.StructuredResult
		}
		cfg.AgentSpecs = append(cfg.AgentSpecs, spec)
	}
	// scope_guard=false means auto-confirm (invert the boolean)
	cfg.AutoConfirmScope = !f.Guards.ScopeGuard
	// ConferenceEnabled was removed (dead code - Conference state is managed
	// via TaskState.Conference field in the engine, not via EngineConfig).
}

type uiConfig struct {
	// Placeholder for future UI configuration (e.g., theme, font size).
}

type teamConfig struct {
	Members []string `toml:"members"` // member IDs to use (default: radical,defender,pragmatic,advocate)
}
