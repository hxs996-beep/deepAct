package config

import (
	"os"
	"path/filepath"
	"strings"
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
	// Isolate HOME so LoadProject's user-level fallback doesn't pick up the
	// developer's real ~/.deepact/config.toml.
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	f := LoadProject(dir)
	if f != nil {
		t.Errorf("expected nil when no config exists, got %+v", f)
	}
}

func TestApply(t *testing.T) {
	cfg := &engine.EngineConfig{
		ModelName:        "my-pro",
		FlashModelName:   "my-flash",
		BaseURL:          "https://custom.api.com",
		MaxContextTokens: 500000,
		RiskThreshold:    0.5,
	}

	f := &File{
		Model: modelConfig{
			Default:         "new-flash",
			Escalation:      "new-pro",
			BaseURL:         "https://new.api.com",
			ReasoningEffort: "low",
		},
		Context: contextConfig{
			MaxBudgetTokens: 200000,
		},
		Routing: routingConfig{
			RiskThreshold: 0.7,
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
	if cfg.ReasoningEffort != "low" {
		t.Errorf("ReasoningEffort = %q, want 'low'", cfg.ReasoningEffort)
	}
	if cfg.MaxContextTokens != 200000 {
		t.Errorf("MaxContextTokens = %d, want 200000", cfg.MaxContextTokens)
	}
	if cfg.RiskThreshold != 0.7 {
		t.Errorf("RiskThreshold = %f, want 0.7", cfg.RiskThreshold)
	}
}

func TestApply_NilFile(t *testing.T) {
	cfg := &engine.EngineConfig{ModelName: "pro"}
	Apply(cfg, nil)
	if cfg.ModelName != "pro" {
		t.Error("Apply with nil file should not change config")
	}
}

// TestApply_AgentToolsUniverseValidation: [agents.<role>].tools naming a
// write-class tool fails loud at startup with a message that names the role
// and guides migration; ["*"] (stripped to empty) means the whole read-only
// universe and must NOT be rejected.
func TestApply_AgentToolsUniverseValidation(t *testing.T) {
	cfg := &engine.EngineConfig{}
	err := Apply(cfg, &File{Agents: map[string]agentRoleConfig{
		"deployer": {Description: "d", Tools: []string{"bash", "edit"}},
	}})
	if err == nil {
		t.Fatal("expected an error for a write-class tool in a role's tools")
	}
	if !strings.Contains(err.Error(), "agents.deployer") || !strings.Contains(err.Error(), "bash") {
		t.Errorf("error must name the role and the offending tool, got %q", err)
	}
	if len(cfg.AgentSpecs) != 0 {
		t.Errorf("a rejected role must not be registered, got %d specs", len(cfg.AgentSpecs))
	}

	cfg2 := &engine.EngineConfig{}
	err = Apply(cfg2, &File{Agents: map[string]agentRoleConfig{
		"scout": {Description: "d", Tools: []string{"*"}},
	}})
	if err != nil {
		t.Fatalf("[\"*\"] means the whole read-only universe and must pass, got %v", err)
	}
	if len(cfg2.AgentSpecs) != 1 || len(cfg2.AgentSpecs[0].ToolNames) != 0 {
		t.Errorf("star must strip to empty ToolNames (full universe), got %+v", cfg2.AgentSpecs)
	}

	cfg3 := &engine.EngineConfig{}
	err = Apply(cfg3, &File{Agents: map[string]agentRoleConfig{
		"scout": {Description: "d", Tools: []string{"read", "grep"}},
	}})
	if err != nil {
		t.Fatalf("universe members must pass, got %v", err)
	}
	if len(cfg3.AgentSpecs[0].ToolNames) != 2 {
		t.Errorf("expected the listed tools kept, got %+v", cfg3.AgentSpecs[0].ToolNames)
	}
}

// TestApply_TokenBudgetThreeState: [context].sub_agent_token_budget and
// [agents.X].token_budget accept -1 (unlimited), 0 (default/inherit), and
// positive caps; anything below -1 is a typo and must fail loud at startup.
func TestApply_TokenBudgetThreeState(t *testing.T) {
	if err := Apply(&engine.EngineConfig{}, &File{Context: contextConfig{SubAgentTokenBudget: -2}}); err == nil {
		t.Fatal("[context].sub_agent_token_budget=-2 must fail loud")
	} else if !strings.Contains(err.Error(), "sub_agent_token_budget") {
		t.Errorf("error must name the knob, got %q", err)
	}

	if err := Apply(&engine.EngineConfig{}, &File{Agents: map[string]agentRoleConfig{
		"scout": {TokenBudget: -2},
	}}); err == nil {
		t.Fatal("[agents.scout].token_budget=-2 must fail loud")
	} else if !strings.Contains(err.Error(), "agents.scout") {
		t.Errorf("error must name the role, got %q", err)
	}

	cfg := &engine.EngineConfig{}
	if err := Apply(cfg, &File{Context: contextConfig{SubAgentTokenBudget: -1}}); err != nil {
		t.Fatalf("-1 must apply, got %v", err)
	}
	if cfg.SubAgentTokenBudget != -1 {
		t.Errorf("explicit unlimited must be preserved, got %d", cfg.SubAgentTokenBudget)
	}
	if err := Apply(cfg, &File{Context: contextConfig{SubAgentTokenBudget: 500}}); err != nil {
		t.Fatalf("positive cap must apply, got %v", err)
	}
	if cfg.SubAgentTokenBudget != 500 {
		t.Errorf("explicit cap must be preserved, got %d", cfg.SubAgentTokenBudget)
	}

	cfg2 := &engine.EngineConfig{}
	if err := Apply(cfg2, &File{Agents: map[string]agentRoleConfig{
		"scout": {Description: "d", TokenBudget: 300},
	}}); err != nil {
		t.Fatalf("role budget must apply, got %v", err)
	}
	if len(cfg2.AgentSpecs) != 1 || cfg2.AgentSpecs[0].TokenBudget != 300 {
		t.Errorf("role budget must reach the spec, got %+v", cfg2.AgentSpecs)
	}

	cfg3 := &engine.EngineConfig{}
	if err := Apply(cfg3, &File{Context: contextConfig{MaxOutstandingAsyncSubAgents: 5}}); err != nil {
		t.Fatalf("outstanding cap must apply, got %v", err)
	}
	if cfg3.MaxOutstandingAsyncSubAgents != 5 {
		t.Errorf("outstanding cap must apply, got %d", cfg3.MaxOutstandingAsyncSubAgents)
	}
}

func TestApply_BaseURL(t *testing.T) {
	cfg := &engine.EngineConfig{}
	f := &File{
		Model: modelConfig{
			BaseURL: "https://custom.com/v1",
		},
	}
	Apply(cfg, f)
	if cfg.BaseURL != "https://custom.com/v1" {
		t.Errorf("BaseURL should use explicit value, got %q", cfg.BaseURL)
	}
}

func TestLoadAPIKey_EnvVarPriority(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-from-env")
	workDir := t.TempDir() // no config file here
	if got := LoadAPIKey(workDir); got != "sk-from-env" {
		t.Errorf("LoadAPIKey = %q, want sk-from-env", got)
	}
}

func TestLoadAPIKey_FromConfig(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".deepact")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := []byte(`
[model]
api_key = "sk-from-config"
default = "flash"
`)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if got := LoadAPIKey(dir); got != "sk-from-config" {
		t.Errorf("LoadAPIKey = %q, want sk-from-config", got)
	}
}

func TestSaveAPIKey_NewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".deepact", "config.toml")
	if err := saveAPIKeyAtPath(path, "sk-new"); err != nil {
		t.Fatalf("saveAPIKeyAtPath: %v", err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.Model.APIKey != "sk-new" {
		t.Errorf("APIKey = %q, want sk-new", f.Model.APIKey)
	}
}

func TestSaveAPIKey_ReplaceExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := []byte(`[model]
default = "flash"
api_key = "sk-old"
escalation = "pro"
`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := saveAPIKeyAtPath(path, "sk-rotated"); err != nil {
		t.Fatalf("saveAPIKeyAtPath: %v", err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.Model.APIKey != "sk-rotated" {
		t.Errorf("APIKey = %q, want sk-rotated", f.Model.APIKey)
	}
	if f.Model.Default != "flash" {
		t.Errorf("Default = %q, want flash (other fields must be preserved)", f.Model.Default)
	}
	if f.Model.Escalation != "pro" {
		t.Errorf("Escalation = %q, want pro (other fields must be preserved)", f.Model.Escalation)
	}
}

func TestSaveAPIKey_AddToExistingModelSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := []byte(`[model]
default = "flash"
`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := saveAPIKeyAtPath(path, "sk-added"); err != nil {
		t.Fatalf("saveAPIKeyAtPath: %v", err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.Model.APIKey != "sk-added" {
		t.Errorf("APIKey = %q, want sk-added", f.Model.APIKey)
	}
	if f.Model.Default != "flash" {
		t.Errorf("Default = %q, want flash", f.Model.Default)
	}
}

func TestLoad_LSPServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := []byte(`
[lsp.servers.python]
command = "pyright-langserver"
args    = ["--stdio"]

[lsp.servers.go]
command = "custom-go-ls"
args    = ["serve"]
language = "go"
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
	if len(f.LSP.Servers) != 2 {
		t.Fatalf("expected 2 LSP server overrides, got %d", len(f.LSP.Servers))
	}
	py, ok := f.LSP.Servers["python"]
	if !ok {
		t.Fatal("expected a python override")
	}
	if py.Command != "pyright-langserver" {
		t.Errorf("python command = %q", py.Command)
	}
	if len(py.Args) != 1 || py.Args[0] != "--stdio" {
		t.Errorf("python args = %v", py.Args)
	}
	go_srv, ok := f.LSP.Servers["go"]
	if !ok {
		t.Fatal("expected a go override")
	}
	if go_srv.Language != "go" {
		t.Errorf("go language = %q", go_srv.Language)
	}
}

func TestLoad_SearchConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := []byte(`
[search]
provider    = "tavily"
api_key     = "tvly-fake"
base_url    = "https://api.tavily.com"
max_results = 8
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
	if f.Search.Provider != "tavily" {
		t.Errorf("Provider = %q", f.Search.Provider)
	}
	if f.Search.APIKey != "tvly-fake" {
		t.Errorf("APIKey = %q", f.Search.APIKey)
	}
	if f.Search.BaseURL != "https://api.tavily.com" {
		t.Errorf("BaseURL = %q", f.Search.BaseURL)
	}
	if f.Search.MaxResults != 8 {
		t.Errorf("MaxResults = %d, want 8", f.Search.MaxResults)
	}
}

func TestApply_UserAgentRoles(t *testing.T) {
	var cfg engine.EngineConfig
	f := &File{
		Agents: map[string]agentRoleConfig{
			"researcher": {
				Description: "只读调研员",
				Persona:     "你是研究员……",
				Tools:       []string{"read", "grep", "glob", "lsp"},
				Model:       "flash",
				MaxIterations: 20,
			},
			"full-tool": {
				Description: "全部工具角色",
				Persona:     "全能",
				Tools:       []string{"*"},
			},
		},
	}
	Apply(&cfg, f)
	if len(cfg.AgentSpecs) != 2 {
		t.Fatalf("AgentSpecs len = %d, want 2", len(cfg.AgentSpecs))
	}
	var researcher, fullTool *engine.AgentSpec
	for i := range cfg.AgentSpecs {
		switch cfg.AgentSpecs[i].ID {
		case "researcher":
			researcher = &cfg.AgentSpecs[i]
		case "full-tool":
			fullTool = &cfg.AgentSpecs[i]
		}
	}
	if researcher == nil {
		t.Fatal("researcher role not found")
	}
	if researcher.Persona != "你是研究员……" {
		t.Errorf("Persona = %q", researcher.Persona)
	}
	if len(researcher.ToolNames) != 4 {
		t.Errorf("ToolNames = %v, want 4 tools", researcher.ToolNames)
	}
	if researcher.ModelName != "flash" {
		t.Errorf("ModelName = %q, want flash", researcher.ModelName)
	}
	if researcher.MaxIterations != 20 {
		t.Errorf("MaxIterations = %d, want 20", researcher.MaxIterations)
	}
	if fullTool == nil {
		t.Fatal("full-tool role not found")
	}
	// tools = ["*"] → empty ToolNames (all tools)
	if len(fullTool.ToolNames) != 0 {
		t.Errorf("full-tool ToolNames = %v, want empty (all tools)", fullTool.ToolNames)
	}
}

func TestLoad_AgentRolesSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := []byte(`
[agents.researcher]
description = "只读调研"
persona = "你是研究员"
tools = ["read", "grep", "glob", "lsp"]
model = "flash"

[agents.fixer]
description = "修复者"
persona = "你是修复者"
tools = ["*"]
structured_result = true
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if f.Agents == nil || len(f.Agents) != 2 {
		t.Fatalf("Agents = %+v, want 2 roles", f.Agents)
	}
	r := f.Agents["researcher"]
	if r.Persona != "你是研究员" || len(r.Tools) != 4 || r.Model != "flash" {
		t.Errorf("researcher = %+v", r)
	}
	fx := f.Agents["fixer"]
	if fx.StructuredResult == nil || !*fx.StructuredResult {
		t.Errorf("fixer.StructuredResult not set true: %+v", fx)
	}
}

// TestApply_MaxSuspendedSubAgents: the suspension cap follows the same shape as
// the async outstanding cap — positive applies, negative fails loud at startup.
func TestApply_MaxSuspendedSubAgents(t *testing.T) {
	cfg := &engine.EngineConfig{}
	if err := Apply(cfg, &File{Context: contextConfig{MaxSuspendedSubAgents: 3}}); err != nil {
		t.Fatalf("max_suspended_subagents must apply, got %v", err)
	}
	if cfg.MaxSuspendedSubAgents != 3 {
		t.Errorf("MaxSuspendedSubAgents = %d, want 3", cfg.MaxSuspendedSubAgents)
	}
	if err := Apply(&engine.EngineConfig{}, &File{Context: contextConfig{MaxSuspendedSubAgents: 2}}); err != nil {
		t.Fatalf("positive cap must apply, got %v", err)
	}
	err := Apply(&engine.EngineConfig{}, &File{Context: contextConfig{MaxSuspendedSubAgents: -1}})
	if err == nil {
		t.Fatal("negative max_suspended_subagents must fail loud")
	}
	if !strings.Contains(err.Error(), "max_suspended_subagents") {
		t.Errorf("error must name the knob, got %q", err)
	}
}
