package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	deeplogconfig "github.com/deepact/deepact/config"
	"github.com/deepact/deepact/context"
	"github.com/deepact/deepact/engine"
	"github.com/deepact/deepact/llm"
	"github.com/deepact/deepact/policy"
	"github.com/deepact/deepact/router"
	"github.com/deepact/deepact/session"
	"github.com/deepact/deepact/skill"
	"github.com/deepact/deepact/tools"
	"github.com/deepact/deepact/tools/builtin"
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
		engineRunner, p, buildErr := buildRunner(apiKey)
		if buildErr != nil {
			return buildErr
		}
		runner = engineRunner
		pricing = p
	}

	ui.SetEngineFactory(func(key string) (ui.EngineRunner, error) {
		r, _, err := buildRunner(key)
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
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI: %w", err)
	}
	return nil
}

func buildRunner(apiKey string) (ui.EngineRunner, engine.PricingConfig, error) {
	if err := storeAPIKey(apiKey); err != nil {
		return nil, engine.PricingConfig{}, fmt.Errorf("store api key: %w", err)
	}
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

// buildSkillSuggestions extracts external skills from the registry and stores
// them as Suggestion objects for the UI slash-completion popup.
func buildSkillSuggestions(reg *skill.Registry) {
	all := reg.All()
	externalSkillSuggestions = make([]ui.Suggestion, 0, len(all))
	seen := make(map[string]bool)
	// Iterate in reverse so external skills (registered later) win over
	// built-in skills (registered first) when names conflict.
	for i := len(all) - 1; i >= 0; i-- {
		s := all[i]
		// Skip built-in skills that users shouldn't need to manually activate,
		// since they auto-match from keywords in the default system prompt.
		if seen[s.Name] || s.Name == "debugging" || s.Name == "verification" {
			continue
		}
		seen[s.Name] = true
		externalSkillSuggestions = append(externalSkillSuggestions, ui.Suggestion{
			Command:     s.Name,
			Description: s.Description,
		})
	}
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
	client, err := buildModelClient(estimator)
	if err != nil {
		return engine.EngineConfig{}, engine.EngineDeps{}, err
	}
	registry := tools.NewRegistry()
	registerBuiltinTools(registry)
	toolExecutor := tools.NewEngineExecutor(registry)
	toolExecutor.ArtifactDir = defaultArtifactDir()

	runner := engine.NewSubAgentRunner(client, toolExecutor, nil, config.ModelName)
	if config.FlashModelName != "" {
		runner.SetFlashModel(config.FlashModelName)
	}
	runner.SetMaxContextTokens(config.MaxContextTokens)
	runner.SetWorkDir(workDir)
	runner.SetSessionID(config.SessionID)

	contextAssembler := context.NewContextAssembler(workDir, estimator)
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

	// Initialize skill registry with built-in methodology skills
	skillReg := skill.NewRegistry(0.45)
	skill.RegisterBuiltinSkills(skillReg)

	// Load external skills from .deepact/skills/ directories.
	// Project-level skills loaded first, then user-level (user wins on conflict).
	projectSkillsDir := filepath.Join(workDir, ".deepact", "skills")
	if home, err := os.UserHomeDir(); err == nil {
		userSkillsDir := filepath.Join(home, ".deepact", "skills")
		externalSkills, err := skill.LoadExternalSkillsFromPaths(projectSkillsDir, userSkillsDir)
		if err != nil {
			return engine.EngineConfig{}, engine.EngineDeps{}, fmt.Errorf("load external skills: %w", err)
		}
		for _, s := range externalSkills {
			skillReg.Register(s)
		}
	} else {
		// fallback: project-level only
		externalSkills, err := skill.LoadExternalSkills(projectSkillsDir)
		if err != nil {
			return engine.EngineConfig{}, engine.EngineDeps{}, fmt.Errorf("load external skills: %w", err)
		}
		for _, s := range externalSkills {
			skillReg.Register(s)
		}
	}

	deps := engine.EngineDeps{
		Model:      client,
		Tools:      toolExecutor,
		Policy:     checker,
		Context:    contextAssembler,
		Compressor: compressor,
		Session:    store,
		Agents:     agentReg,
		Skills:     skillReg,
		Router:     router.NewRouter(0.55),
	}
	// Apply model names from config to router
	deps.Router.(*router.DefaultRouter).ModelName = config.ModelName
	deps.Router.(*router.DefaultRouter).FlashModelName = config.FlashModelName
	return config, deps, nil
}

func buildModelClient(estimator *llm.TokenEstimator) (*llm.EngineClient, error) {
	apiKey, err := loadAPIKey()
	if err != nil {
		return nil, fmt.Errorf("API key: %w", err)
	}
	client := llm.NewDeepSeekClient(apiKey, nil, nil, llm.DefaultRetryPolicy(), estimator)
	return llm.NewEngineClient(client), nil
}

func registerBuiltinTools(registry *tools.Registry) {
	registry.Register(builtin.NewReadTool())
	registry.Register(builtin.NewWriteTool())
	registry.Register(builtin.NewEditTool())
	registry.Register(builtin.NewGrepTool())
	registry.Register(builtin.NewGlobTool())
	registry.Register(builtin.NewBashTool())
	registry.Register(builtin.NewRevertTool())
	registry.Register(builtin.NewFetchTool())
}

func defaultEngineConfig() engine.EngineConfig {
	return engine.EngineConfig{
		ModelName:              "deepseek-v4-pro",
		FlashModelName:         "deepseek-v4-flash",
		MaxTurns:               999,
		MaxIterationsPerTurn:   15,
		MaxContextTokens:       1048576,
		PlanningEnabled:        true,
		PlanningThresholdChars: 120,
		AutoConfirmScope:       false, // scope guard now actually prompts user before destructive edits
		ConferenceEnabled:      true,
		Pricing: engine.PricingConfig{
			Models: map[string]engine.ModelPricing{
				"deepseek-v4-pro": {
					InputPricePerToken:         0.000003,    // ¥3/百万
					OutputPricePerToken:        0.000006,    // ¥6/百万
					CacheHitInputPricePerToken: 0.000000025, // ¥0.025/百万
				},
				"deepseek-v4-flash": {
					InputPricePerToken:         0.000001,   // ¥1/百万
					OutputPricePerToken:        0.000002,   // ¥2/百万
					CacheHitInputPricePerToken: 0.00000002, // ¥0.02/百万
				},
			},
			Default: engine.ModelPricing{
				InputPricePerToken:         0.000003,
				OutputPricePerToken:        0.000006,
				CacheHitInputPricePerToken: 0.000000025,
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
