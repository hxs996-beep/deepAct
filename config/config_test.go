package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/deepact/deepact/engine"
)

func TestLoad_FileNotFound(t *testing.T) {
	f, err := Load("/nonexistent/path/config.toml")
	if err != nil {
		t.Fatalf("unexpected error for nonexistent file: %v", err)
	}
	if f != nil {
		t.Errorf("expected nil config for nonexistent file, got %+v", f)
	}
}

func TestLoad_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := []byte(`
[model]
default = "deepseek-v4-flash"
escalation = "deepseek-v4-pro"
base_url = "https://api.deepseek.com"

[context]
max_budget_tokens = 100000

[guards]
scope_guard = false

[routing]
risk_threshold = 0.5
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if f == nil {
		t.Fatal("expected non-nil File")
	}
	if f.Model.Default != "deepseek-v4-flash" {
		t.Errorf("Model.Default = %q, want 'deepseek-v4-flash'", f.Model.Default)
	}
	if f.Model.Escalation != "deepseek-v4-pro" {
		t.Errorf("Model.Escalation = %q", f.Model.Escalation)
	}
	if f.Model.BaseURL != "https://api.deepseek.com" {
		t.Errorf("Model.BaseURL = %q", f.Model.BaseURL)
	}
	if f.Context.MaxBudgetTokens != 100000 {
		t.Errorf("Context.MaxBudgetTokens = %d, want 100000", f.Context.MaxBudgetTokens)
	}
	if f.Guards.ScopeGuard {
		t.Error("Guards.ScopeGuard should be false")
	}
	if f.Routing.RiskThreshold != 0.5 {
		t.Errorf("Routing.RiskThreshold = %f, want 0.5", f.Routing.RiskThreshold)
	}
}

func TestLoad_NotExistReturnsNil(t *testing.T) {
	f, err := Load("/nonexistent/deepact/config.toml")
	if err != nil {
		t.Fatalf("Load should return nil,nil for missing file: %v", err)
	}
	if f != nil {
		t.Errorf("expected nil, got %+v", f)
	}
}

func TestLoadProject_Found(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".deepact")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	content := []byte(`
[model]
default = "flash"
escalation = "pro"
`)
	if err := os.WriteFile(cfgPath, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	f := LoadProject(dir)
	if f == nil {
		t.Fatal("expected non-nil File from LoadProject")
	}
	if f.Model.Default != "flash" {
		t.Errorf("Model.Default = %q, want 'flash'", f.Model.Default)
	}
	if f.Model.Escalation != "pro" {
		t.Errorf("Model.Escalation = %q, want 'pro'", f.Model.Escalation)
	}
}

func TestLoadProject_NotFound(t *testing.T) {
	dir := t.TempDir()
	f := LoadProject(dir)
	if f != nil {
		t.Errorf("expected nil when no config exists, got %+v", f)
	}
}

func TestApply(t *testing.T) {
	cfg := &engine.EngineConfig{
		ModelName:      "my-pro",
		FlashModelName: "my-flash",
		BaseURL:        "https://custom.api.com",
		MaxContextTokens: 500000,
		RiskThreshold:  0.5,
		AutoConfirmScope: false,
	}

	f := &File{
		Model: modelConfig{
			Default:    "new-flash",
			Escalation: "new-pro",
			BaseURL:    "https://new.api.com",
		},
		Context: contextConfig{
			MaxBudgetTokens: 200000,
		},
		Routing: routingConfig{
			RiskThreshold: 0.7,
		},
		Guards: guardsConfig{
			ScopeGuard: false, // = auto-confirm scope = true
		},
	}

	Apply(cfg, f)
	if cfg.FlashModelName != "new-flash" {
		t.Errorf("FlashModelName = %q, want 'new-flash'", cfg.FlashModelName)
	}
	if cfg.ModelName != "new-pro" {
		t.Errorf("ModelName = %q, want 'new-pro'", cfg.ModelName)
	}
	if cfg.BaseURL != "https://new.api.com" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.MaxContextTokens != 200000 {
		t.Errorf("MaxContextTokens = %d, want 200000", cfg.MaxContextTokens)
	}
	if cfg.RiskThreshold != 0.7 {
		t.Errorf("RiskThreshold = %f, want 0.7", cfg.RiskThreshold)
	}
	if !cfg.AutoConfirmScope {
		t.Error("AutoConfirmScope should be true (scope_guard=false)")
	}
}

func TestApply_NilFile(t *testing.T) {
	cfg := &engine.EngineConfig{ModelName: "pro"}
	Apply(cfg, nil)
	if cfg.ModelName != "pro" {
		t.Error("Apply with nil file should not change config")
	}
}

func TestApply_ProviderURLMapping(t *testing.T) {
	cfg := &engine.EngineConfig{}
	f := &File{
		Model: modelConfig{
			Provider: "deepseek",
		},
	}
	Apply(cfg, f)
	if cfg.BaseURL != "https://api.deepseek.com/chat/completions" {
		t.Errorf("BaseURL = %q, want deepseek endpoint", cfg.BaseURL)
	}
}

func TestApply_ProviderOverriddenByBaseURL(t *testing.T) {
	cfg := &engine.EngineConfig{}
	f := &File{
		Model: modelConfig{
			Provider: "openrouter",
			BaseURL:  "https://custom.com/v1",
		},
	}
	Apply(cfg, f)
	// BaseURL takes priority over provider
	if cfg.BaseURL != "https://custom.com/v1" {
		t.Errorf("BaseURL should use explicit value, got %q", cfg.BaseURL)
	}
}

func TestResolveProviderURL(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     string
	}{
		{
			name:     "deepseek",
			provider: "deepseek",
			want:     "https://api.deepseek.com/chat/completions",
		},
		{
			name:     "openrouter",
			provider: "openrouter",
			want:     "https://openrouter.ai/api/v1",
		},
		{
			name:     "unknown",
			provider: "ollama",
			want:     "",
		},
		{
			name:     "empty",
			provider: "",
			want:     "",
		},
	}
	for _, tt := range tests {
		got := resolveProviderURL(tt.provider)
		if got != tt.want {
			t.Errorf("%s: resolveProviderURL(%q) = %q, want %q", tt.name, tt.provider, got, tt.want)
		}
	}
}
