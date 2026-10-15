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

// TestLoad_RemovedKeyFailsLoud: a config key that no longer exists must stop the
// process with a message naming it. Silently ignoring a removed guard knob is how
// a user ends up believing in a limit that is no longer enforced.
func TestLoad_RemovedKeyFailsLoud(t *testing.T) {
	for _, body := range []string{
		"[agents.scout]\nmax_iterations = 15\n",
		"[agents.scout]\ntoken_budget = 300\n",
		"[context]\nsub_agent_token_budget = -1\n",
		"[context]\nrun_sub_agent_token_budget = 2000\n",
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := Load(path)
		if err == nil {
			t.Fatalf("a removed key must fail loud, config:\n%s", body)
		}
		if !strings.Contains(err.Error(), "removed") {
			t.Errorf("error must say the key was removed, got %q", err)
		}
	}
}

// TestLoad_TypoKeyFailsLoud: any key nothing reads is an error, not a silent
// no-op — the same class of bug as a removed key.
func TestLoad_TypoKeyFailsLoud(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[model]\napi_ky = \"x\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := func() error { _, e := Load(path); return e }()
	if err == nil {
		t.Fatal("a typo'd key must fail loud")
	}
	if !strings.Contains(err.Error(), "api_ky") {
		t.Errorf("error must name the unknown key, got %q", err)
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

// TestApply_DelegationTreeLimits: the three delegation-tree caps are applied
// when set and left at their engine defaults (0 = default) when omitted.
func TestApply_DelegationTreeLimits(t *testing.T) {
	cfg := &engine.EngineConfig{}
	err := Apply(cfg, &File{Context: contextConfig{
		MaxConcurrentSubAgents: 4,
		MaxSubAgentDepth:       3,
	}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cfg.MaxConcurrentSubAgents != 4 {
		t.Errorf("MaxConcurrentSubAgents = %d, want 4", cfg.MaxConcurrentSubAgents)
	}
	if cfg.MaxSubAgentDepth != 3 {
		t.Errorf("MaxSubAgentDepth = %d, want 3", cfg.MaxSubAgentDepth)
	}
}

// TestApply_DelegationTreeLimits_Omitted: 0 means "use the engine default", so
// omitting the keys must not write anything into the config.
func TestApply_DelegationTreeLimits_Omitted(t *testing.T) {
	cfg := &engine.EngineConfig{}
	if err := Apply(cfg, &File{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cfg.MaxConcurrentSubAgents != 0 || cfg.MaxSubAgentDepth != 0 {
		t.Errorf("omitted keys must stay 0, got %d/%d",
			cfg.MaxConcurrentSubAgents, cfg.MaxSubAgentDepth)
	}
}

// TestApply_DelegationTreeLimits_Invalid: a zero/negative width or depth and a
// token budget below -1 are typos — fail loud at startup, naming the key and
// the legal values (the form every other [context] key uses).
func TestApply_DelegationTreeLimits_Invalid(t *testing.T) {
	cases := []struct {
		name string
		f    *File
		want string
	}{
		{"negative width", &File{Context: contextConfig{MaxConcurrentSubAgents: -1}}, "max_concurrent_subagents"},
		{"negative depth", &File{Context: contextConfig{MaxSubAgentDepth: -2}}, "max_subagent_depth"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Apply(&engine.EngineConfig{}, tc.f)
			if err == nil {
				t.Fatalf("expected a startup error naming %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error must name the key %q, got %q", tc.want, err)
			}
		})
	}
}

// TestApply_DelegationTreeLimits_Omitted_NoBudgetKey documents the removal of the
// Run-level token budget: [context].run_sub_agent_token_budget no longer exists,
// and the main agent has no spend cap at all.
func TestApply_DelegationTreeLimits_Omitted_NoBudgetKey(t *testing.T) {
	cfg := &engine.EngineConfig{}
	if err := Apply(cfg, &File{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cfg.MaxSubAgentDepth != 0 || cfg.MaxConcurrentSubAgents != 0 {
		t.Errorf("unexpected limits: depth=%d width=%d", cfg.MaxSubAgentDepth, cfg.MaxConcurrentSubAgents)
	}
}

// TestApply_AgentTextResult: [agents.x] text_result declares that a role's text
// reply IS its result, and must survive Apply onto the AgentSpec.
func TestApply_AgentTextResult(t *testing.T) {
	yes := true
	cfg := &engine.EngineConfig{}
	err := Apply(cfg, &File{Agents: map[string]agentRoleConfig{
		"prop": {Description: "d", TextResult: &yes},
	}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(cfg.AgentSpecs) != 1 {
		t.Fatalf("expected 1 role spec, got %d", len(cfg.AgentSpecs))
	}
	if !cfg.AgentSpecs[0].TextResult {
		t.Error("text_result must apply to the role spec")
	}
}

// The two completion contracts contradict: structured means "only submit_result
// counts", text means "the text reply is the result". Fail loud rather than
// silently picking one.
func TestApply_AgentTextResult_ConflictsWithStructured(t *testing.T) {
	yes := true
	err := Apply(&engine.EngineConfig{}, &File{Agents: map[string]agentRoleConfig{
		"prop": {TextResult: &yes, StructuredResult: &yes},
	}})
	if err == nil {
		t.Fatal("text_result + structured_result must fail loud")
	}
	if !strings.Contains(err.Error(), "text_result") {
		t.Errorf("error must name the key, got %q", err)
	}
}
