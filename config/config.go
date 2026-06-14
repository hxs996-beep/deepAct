// Package config loads project configuration from .deepact/config.toml.
package config

import (
	"os"
	"path/filepath"

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
	UI         uiConfig         `toml:"ui"`
}

type modelConfig struct {
	Default    string `toml:"default"`
	Escalation string `toml:"escalation"`
	Provider   string `toml:"provider"`   // "deepseek" or "openrouter"
	BaseURL    string `toml:"base_url"`   // overrides provider if set
}

type routingConfig struct {
	RiskThreshold float64 `toml:"risk_threshold"`
}

type contextConfig struct {
	MaxBudgetTokens int `toml:"max_budget_tokens"`
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
	if f.Model.Provider != "" && f.Model.BaseURL == "" {
		cfg.BaseURL = resolveProviderURL(f.Model.Provider)
	}
	if f.Model.BaseURL != "" {
		cfg.BaseURL = f.Model.BaseURL
	}
	if f.Context.MaxBudgetTokens > 0 {
		cfg.MaxContextTokens = f.Context.MaxBudgetTokens
	}
	if f.Routing.RiskThreshold > 0 {
		cfg.RiskThreshold = f.Routing.RiskThreshold
	}
	// scope_guard=false means auto-confirm (invert the boolean)
	cfg.AutoConfirmScope = !f.Guards.ScopeGuard
	// ConferenceEnabled was removed (dead code - Conference state is managed
	// via TaskState.Conference field in the engine, not via EngineConfig).
}

type uiConfig struct {
	// Placeholder for future UI configuration (e.g., theme, font size).
}

func resolveProviderURL(provider string) string {
	switch provider {
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "deepseek":
		return "https://api.deepseek.com/chat/completions"
	default:
		return ""
	}
}
