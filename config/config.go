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
	Model      modelConfig      `toml:"model"`
	Routing    routingConfig    `toml:"routing"`
	Context    contextConfig    `toml:"context"`
	Guards     guardsConfig     `toml:"guards"`
	// Conference field removed — ConferenceEnabled was dead code (never read by engine).
	// Conference state is managed via TaskState.Conference in the engine package.
	Team       teamConfig       `toml:"team"`
	UI         uiConfig         `toml:"ui"`
}

type modelConfig struct {
	Default        string `toml:"default"`
	Escalation     string `toml:"escalation"`
	BaseURL        string `toml:"base_url"`      // API base URL (e.g. https://api.deepseek.com). Defaults to DeepSeek official.
	SubAgentURL    string `toml:"sub_agent_url"` // separate endpoint for sub-agents (cache isolation)
	APIKey         string `toml:"api_key"`       // DeepSeek/OpenRouter API key
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
	if f.Model.SubAgentURL != "" {
		cfg.SubAgentBaseURL = f.Model.SubAgentURL
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
	// scope_guard=false means auto-confirm (invert the boolean)
	cfg.AutoConfirmScope = !f.Guards.ScopeGuard
	// ConferenceEnabled was removed (dead code - Conference state is managed
	// via TaskState.Conference field in the engine, not via EngineConfig).
	if len(f.Team.Members) > 0 {
		cfg.TeamMembers = f.Team.Members
	}
}

type uiConfig struct {
	// Placeholder for future UI configuration (e.g., theme, font size).
}

type teamConfig struct {
	Members []string `toml:"members"` // member IDs to use (default: radical,defender,pragmatic,advocate)
}
