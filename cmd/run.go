package cmd

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	deeplogconfig "github.com/deepact/deepact/config"
	deeplogcontext "github.com/deepact/deepact/context"
	"github.com/deepact/deepact/engine"
	"github.com/deepact/deepact/llm"
	"github.com/deepact/deepact/policy"
	"github.com/deepact/deepact/router"
	"github.com/deepact/deepact/session"
	"github.com/deepact/deepact/skill"
	"github.com/deepact/deepact/tools"
	"github.com/deepact/deepact/tools/builtin"
	"github.com/deepact/deepact/tools/mcp"
	"github.com/deepact/deepact/ui"
)

func runInteractive(cmd *cobra.Command, args []string) error {
	apiKey, err := loadAPIKey()
	if err != nil {
		return err
	}

	var runner ui.EngineRunner
	var pricing engine.PricingConfig
	if apiKey != "" {
		engineRunner, p, buildErr := buildRunner()
		if buildErr != nil {
			return buildErr
		}
		runner = engineRunner
		pricing = p
	}

	ui.SetEngineFactory(func(key string) (ui.EngineRunner, error) {
		r, _, err := buildRunner()
		return r, err
	})

	// Prevent charmbracelet/termenv from sending OSC terminal queries.
	// Without this, termenv.HasDarkBackground() sends \x1b]11;?\x07 and the
	// response (containing hex color values like "fae0") leaks into stdin,
	// corrupting user input.
	if os.Getenv("COLORFGBG") == "" {
		os.Setenv("COLORFGBG", "15;0")
	}

	// Pre-flight: validate API key works before entering TUI.
	// Avoids the frustrating experience of describing a task only to discover
	// the key is invalid.
	if runner != nil {
		if err := runner.ValidateConnection(); err != nil {
			fmt.Fprintf(os.Stderr, "❌ API connection failed: %v\n", err)
			fmt.Fprintf(os.Stderr, "   Check your DEEPSEEK_API_KEY with: deepact set apikey\n")
			return err
		}
	}

	model := ui.NewModel(runner, pricing)
	model.SetSkillSuggestions(externalSkillSuggestions)
	// Mouse interaction: wheel scrolling + drag-to-select.
	// Left-click drag selects text and auto-copies to clipboard on release.
	// On Windows ConPTY, WithMouseAllMotion (mode 1003) is used instead of
	// WithMouseCellMotion (mode 1002) because ConPTY's mode 1002 encoding
	// drops wheel events and fragments SGR motion sequences, causing
	// stuttery selection and non-functional scrolling.
	mouseOpt := tea.WithMouseCellMotion
	if runtime.GOOS == "windows" {
		mouseOpt = tea.WithMouseAllMotion
	}
	opts := []tea.ProgramOption{tea.WithAltScreen(), mouseOpt()}
	p := tea.NewProgram(model, opts...)
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI: %w", err)
	}

	// Close MCP server connections after the session ends
	if runner, ok := runner.(*ui.ProgressEngineRunner); ok {
		for _, c := range runner.Deps.MCPManagers {
			c.Close()
		}
	}
	return nil
}

func buildRunner() (ui.EngineRunner, engine.PricingConfig, error) {
	config, deps, err := buildEngineDeps()
	if err != nil {
		return nil, engine.PricingConfig{}, err
	}
	// Cache external skill suggestions for the UI slash-completion popup.
	// skillReg.All() returns ALL registered skills (built-in + external);
	// we only expose non-builtin ones as /-prefixed suggestions.
	buildSkillSuggestions(deps.Skills)
	return &ui.ProgressEngineRunner{Config: config, Deps: deps}, config.Pricing, nil
}

// externalSkillSuggestions caches the list of external skill entries so the TUI
// model can show them in the /-completion popup without importing the skill package.
var externalSkillSuggestions []ui.Suggestion

// buildSkillSuggestions extracts skills from the registry and stores
// them as Suggestion objects for the UI slash-completion popup.
func buildSkillSuggestions(reg *skill.Registry) {
	all := reg.All()
	externalSkillSuggestions = make([]ui.Suggestion, 0, len(all))
	seen := make(map[string]bool)
	// Iterate in reverse so external skills (registered later) win over
	// built-in skills (registered first) when names conflict.
	for i := len(all) - 1; i >= 0; i-- {
		s := all[i]
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		externalSkillSuggestions = append(externalSkillSuggestions, ui.Suggestion{
			Command:     s.Name,
			Description: s.Description,
		})
	}
}

// buildSkillsBlock renders a static skills hint for the stable zone.
// Each skill is shown as "name: description" so the model knows what each does.
// Dynamic skill suggestions (matched by keyword per-turn) are injected
// separately via pendingPinnedMessages in the engine loop.
func buildSkillsBlock(all []*skill.Skill) string {
	if len(all) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Available Skills\n")
	b.WriteString("Type `/<skillname>` (e.g., `/brainstorming`) to activate a specific skill. ")
	b.WriteString("Use `activate_skill` to switch skills when the current one reaches its terminal state. ")
	b.WriteString("Relevant skills for your task are suggested below.\n\n")
	for _, s := range all {
		b.WriteString("- **")
		b.WriteString(s.Name)
		b.WriteString("**: ")
		if s.Description != "" {
			b.WriteString(s.Description)
		} else {
			b.WriteString("(no description)")
		}
		b.WriteString("\n")

		// Keywords — what triggers this skill
		if len(s.Keywords) > 0 {
			b.WriteString("  Keywords: ")
			b.WriteString(strings.Join(s.Keywords, ", "))
			b.WriteString("\n")
		}

		// Next skills in chain — LLM uses this to know what to activate next
		if len(s.NextSkills) > 0 && !(len(s.NextSkills) == 1 && s.NextSkills[0] == "") {
			b.WriteString("  → Next: ")
			b.WriteString(strings.Join(s.NextSkills, ", "))
			b.WriteString("\n")
		}

		// Auto-activation threshold
		if s.AutoActivateThreshold != nil {
			b.WriteString(fmt.Sprintf("  Auto-activate: ≥%d keyword matches\n", *s.AutoActivateThreshold))
		}

		b.WriteString("\n")
	}
	return b.String()
}

func buildEngineDeps() (engine.EngineConfig, engine.EngineDeps, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return engine.EngineConfig{}, engine.EngineDeps{}, fmt.Errorf("get working dir: %w", err)
	}
	config := defaultEngineConfig()
	config.WorkDir = workDir

	// Load and apply .deepact/config.toml over defaults
	if f := deeplogconfig.LoadProject(workDir); f != nil {
		deeplogconfig.Apply(&config, f)
	}
	config.SessionID = fmt.Sprintf("session-%d", time.Now().UnixNano())

	estimator := llm.NewTokenEstimator()
	client, err := buildModelClient(estimator, config.BaseURL)
	if err != nil {
		return engine.EngineConfig{}, engine.EngineDeps{}, err
	}
	registry := tools.NewRegistry()
	registerBuiltinTools(registry)

	// Start MCP servers and register their tools
	var mcpManagers []*mcp.ManagedServer
	if err := registerMCPTools(registry, workDir, &mcpManagers); err != nil {
		return engine.EngineConfig{}, engine.EngineDeps{}, fmt.Errorf("MCP: %w", err)
	}

	toolExecutor := tools.NewEngineExecutor(registry)
	toolExecutor.ArtifactDir = defaultArtifactDir()

	runner := engine.NewSubAgentRunner(client, toolExecutor, nil, config.ModelName)
	// Always give sub-agents their own API endpoint for prefix cache isolation.
	// If explicitly configured (SubAgentBaseURL), use that; otherwise auto-derive
	// from the main agent's endpoint by appending a harmless query parameter.
	if config.SubAgentBaseURL != "" {
		runner.SetSubAgentBaseURL(config.SubAgentBaseURL)
	} else {
		apiKey, _ := loadAPIKey()
		runner.SetSubAgentBaseURL(llm.SubAgentEndpoint(config.BaseURL, apiKey))
	}
	if config.FlashModelName != "" {
		runner.SetFlashModel(config.FlashModelName)
	}
	runner.SetMaxContextTokens(config.MaxContextTokens)
	runner.SetMaxOutputTokens(config.MaxOutputTokens)
	runner.SetWorkDir(workDir)
	runner.SetSessionID(config.SessionID)

	// Pre-compute language packs for sub-agent system prompt (zh + en).
	// User language is detected per-session, so both variants are cached here.
	projLang := deeplogcontext.DetectLanguage(workDir)
	runner.SetLangPacks(deeplogcontext.GetLangPack(projLang, "中文"), deeplogcontext.GetLangPack(projLang, ""))

	contextAssembler := deeplogcontext.NewContextAssembler(workDir, estimator)

	compressor := engine.NewCompressionOrchestrator(client, contextAssembler, config.ModelName)
	if config.FlashModelName != "" {
		compressor.SetFlashModelName(config.FlashModelName)
	}
	runner.SetCompressor(compressor)

	agentReg := engine.NewDefaultRegistry(runner)
	runner.SetRegistry(agentReg)

	checker := policy.NewChecker(0.45)
	checker.SetModelClient(client)
	checker.SetModelName(config.ModelName)

	store, err := session.NewStore(defaultSessionDir())
	if err != nil {
		return engine.EngineConfig{}, engine.EngineDeps{}, err
	}

	// Initialize skill registry from user-installed skills in ~/.deepact/skills/.
	skillReg := skill.NewRegistry()
	if home, err := os.UserHomeDir(); err == nil {
		userSkillsDir := filepath.Join(home, ".deepact", "skills")
		userSkills, err := skill.LoadExternalSkills(userSkillsDir)
		if err != nil {
			return engine.EngineConfig{}, engine.EngineDeps{}, fmt.Errorf("load user skills: %w", err)
		}
		for _, s := range userSkills {
			skillReg.Register(s)
		}
	}

	// Register the skill_install tool so users can install skills from the community registry.
	if home, err := os.UserHomeDir(); err == nil {
		userSkillsDir := filepath.Join(home, ".deepact", "skills")
		registry.Register(builtin.NewSkillInstallTool(userSkillsDir, skillReg))
	}

	// Build rendered skills list for stable system prompt block
	skillsBlock := buildSkillsBlock(skillReg.All())
	contextAssembler.SetSkillsBlock(skillsBlock)

	// Create model router for Pro/Flash routing decisions
	routing := router.NewRouter(config.RiskThreshold)
	if config.ModelName != "" {
		routing.ModelName = config.ModelName
	}
	if config.FlashModelName != "" {
		routing.FlashModelName = config.FlashModelName
	}

	// Build skill matcher: keyword-first with LLM semantic fallback.
	// The semantic matcher wraps the model client for flash-model calls.
	kwMatcher := skill.NewKeywordMatcher(skillReg)
	var semMatcher *skill.SemanticMatcher
	if config.FlashModelName != "" && client != nil {
		matchFn := func(ctx context.Context, systemMsg, userMsg string) (string, error) {
			req := engine.ModelRequest{
				Model: config.FlashModelName,
				Messages: []engine.ModelMessage{
					{Role: "system", Content: systemMsg},
					{Role: "user", Content: userMsg},
				},
				Temperature: 0,
				MaxTokens:   64,
			}
			resp, err := client.Complete(ctx, req)
			if err != nil {
				return "", err
			}
			return resp.Message.Content, nil
		}
		semMatcher = skill.NewSemanticMatcher(matchFn, config.FlashModelName)
	}
	skillMatcher := skill.NewFallbackMatcher(kwMatcher, semMatcher)

	deps := engine.EngineDeps{
		Model:      client,
		Tools:      toolExecutor,
		Policy:     checker,
		Context:    contextAssembler,
		Compressor: compressor,
		Session:    store,
		Agents:     agentReg,
		Skills:     skillReg,
		SkillMatcher: skillMatcher,
		Router:     routing,
	}
	// Store MCP managers (as io.Closer) for cleanup on shutdown
	mcpClosers := make([]io.Closer, len(mcpManagers))
	for i := range mcpManagers {
		mcpClosers[i] = mcpManagers[i]
	}
	deps.MCPManagers = mcpClosers
	return config, deps, nil
}

// registerMCPTools loads MCP server config, starts each server, and
// registers their tools in the tool registry with a "<server>_" prefix.
func registerMCPTools(registry *tools.Registry, workDir string, managers *[]*mcp.ManagedServer) error {
	cfg, err := mcp.LoadConfig(workDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg == nil {
		return nil // no MCP servers configured
	}

	for _, sc := range cfg.Servers {
		manager, err := mcp.StartServer(sc)
		if err != nil {
			log.Printf("⚠️  MCP server %q failed to start, skipping: %v", sc.Name, err)
			continue
		}
		*managers = append(*managers, manager)

		for _, mt := range manager.Tools() {
			wrapper := &mcp.ToolWrapper{
				Client:    manager.Client,
				MCPServer: sc.Name,
				MCPTool:   mt,
			}
			registry.Register(wrapper)
		}
	}
	return nil
}

func buildModelClient(estimator *llm.TokenEstimator, baseURL string) (*llm.EngineClient, error) {
	apiKey, err := loadAPIKey()
	if err != nil {
		return nil, fmt.Errorf("API key: %w", err)
	}
	endpoint := llm.DetectBaseURL(baseURL, apiKey)
	client := llm.NewDeepSeekClientWithEndpoint(endpoint, apiKey, nil, nil, llm.DefaultRetryPolicy(), estimator)
	return llm.NewEngineClient(client), nil
}


func registerBuiltinTools(registry *tools.Registry) {
	registry.Register(builtin.NewReadTool())
	registry.Register(builtin.NewReadMultiTool())
	registry.Register(builtin.NewWriteTool())
	registry.Register(builtin.NewEditTool())
	registry.Register(builtin.NewGrepTool())
	registry.Register(builtin.NewGlobTool())
	registry.Register(builtin.NewBashTool())
	registry.Register(builtin.NewRevertTool())
	registry.Register(builtin.NewFetchTool())
	registry.Register(builtin.NewLSPTool())
}

func defaultEngineConfig() engine.EngineConfig {
	env := os.Getenv("DEEPACT_ENV")
	devMode := env == "dev" || env == "development"

	if devMode {
		return engine.EngineConfig{
			ModelName:              "deepseek/deepseek-chat",
			FlashModelName:         "deepseek/deepseek-chat",
			BaseURL:                llm.DefaultOpenRouterURL,
			MaxTurns:               999,
			MaxIterationsPerTurn:   15,
			MaxContextTokens:       1048576,
			PlanningEnabled:        true,
			PlanningThresholdChars: 120,
			AutoConfirmScope:       false,
			// ConferenceEnabled removed (dead code - Conference state managed via TaskState.Conference)
			RiskThreshold:          0.55,
			Pricing: engine.PricingConfig{
				Models: map[string]engine.ModelPricing{
					"deepseek/deepseek-chat": {
						InputPricePerToken:         0.000001,
						OutputPricePerToken:        0.000002,
						CacheHitInputPricePerToken: 0.0000005,
					},
				},
				Default: engine.ModelPricing{
					InputPricePerToken:         0.000001,
					OutputPricePerToken:        0.000002,
					CacheHitInputPricePerToken: 0.0000005,
				},
			},
		}
	}

	return engine.EngineConfig{
		ModelName:              "deepseek-v4-flash",
		FlashModelName:         "deepseek-v4-flash",
		BaseURL:                llm.DefaultDeepSeekEndpoint,
		MaxTurns:               999,
		MaxIterationsPerTurn:   15,
		MaxContextTokens:       1048576,
		PlanningEnabled:        true,
		PlanningThresholdChars: 120,
		AutoConfirmScope:       false,
		// ConferenceEnabled removed (dead code - Conference state managed via TaskState.Conference)
		RiskThreshold:          0.55,
		Pricing: engine.PricingConfig{
			Models: map[string]engine.ModelPricing{
				"deepseek-v4-flash": {
					InputPricePerToken:         0.000001,
					OutputPricePerToken:        0.000002,
					CacheHitInputPricePerToken: 0.00000002,
				},
			},
			Default: engine.ModelPricing{
				InputPricePerToken:         0.000001,
				OutputPricePerToken:        0.000002,
				CacheHitInputPricePerToken: 0.00000002,
			},
		},
	}
}

func defaultSessionDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".deepact", "sessions")
	}
	return filepath.Join(os.TempDir(), "deepact", "sessions")
}

func defaultArtifactDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".deepact", "artifacts")
	}
	return filepath.Join(os.TempDir(), "deepact", "artifacts")
}
