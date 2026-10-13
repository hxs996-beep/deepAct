package context

import (
	"os"
	"runtime"
	"time"

	"github.com/deepact/deepact/context/promptset"
	"github.com/deepact/deepact/engine"
	"github.com/deepact/deepact/llm"
)

type ContextAssembler struct {
	systemPrompt       string // canonical system prompt, built once and cached
	projectRoot        string
	estimator          *llm.TokenEstimator
	envInfo            EnvironmentInfo // session-stable env info, built once at startup
	stableSessionBlock string         // built once from envInfo, cached for cache stability
	skillsBlock        string         // built once from skill registry, cached for cache stability
	agentsBlock        string         // rendered AGENTS.md content in the stable zone; built once at startup, cached
}

func NewContextAssembler(projectRoot string, estimator *llm.TokenEstimator) *ContextAssembler {
	if estimator == nil {
		estimator = llm.NewTokenEstimator()
	}

	return &ContextAssembler{
		projectRoot: projectRoot,
		estimator:   estimator,
		envInfo:     buildEnvironmentInfo(projectRoot),
	}
}

// SetSkillsBlock sets the rendered skills list for inclusion in the stable zone.
// Called once at startup from cmd/run.go after the skill registry is built.
// The skills block is cached and included as a stable message after Block S.
func (a *ContextAssembler) SetSkillsBlock(s string) {
	a.skillsBlock = s
}

// SetAgentsBlock sets the rendered AGENTS.md content for inclusion in the
// stable zone. Called once at startup from cmd/run.go after LoadAgentsMD.
// Empty string means no AGENTS.md files were found (output identical to current).
func (a *ContextAssembler) SetAgentsBlock(rendered string) {
	a.agentsBlock = rendered
}

// InjectedBlocks returns the prompt blocks this assembler injects into the
// model input: the system prompt, the stable session context (Block S +
// AGENTS.md) and the skills list. The engine derives its echo-stripping
// headers from these blocks, so the set cannot desync from what is sent —
// unlike the previous hand-maintained header list it replaces.
// Blocks not built yet (no user message / no skills) are omitted.
func (a *ContextAssembler) InjectedBlocks() []string {
	blocks := make([]string, 0, 3)
	for _, b := range []string{a.systemPrompt, a.stableSessionBlock, a.skillsBlock} {
		if b != "" {
			blocks = append(blocks, b)
		}
	}
	return blocks
}

func (a *ContextAssembler) Build(state *engine.TaskState, history []engine.Message, toolResults []engine.ToolResult) []engine.ModelMessage {
	messages := make([]engine.ModelMessage, 0, len(history)+6)

	// === STABLE ZONE (TOP — prefix cache friendly) ===
	// Message 1: System prompt — identical every turn → cached ✓
	// Built once from the single canonical (English) prompt set. The prompt is
	// language-independent, so no per-session language gate is needed.
	if a.systemPrompt == "" {
		langPacks := GetLangPacks(DetectLanguages(a.projectRoot))
		prompts := promptset.Get()
		a.systemPrompt = prompts.System + "\n\n# Language Pack\n" + langPacks + "\n\n" + prompts.Examples
	}
	// The skills catalog ships inside the system message so the model treats it
	// as a system-level instruction; whether to load a skill stays the model's own
	// decision. The block is stable across the session, so the system prefix still
	// caches as one piece.
	prompt := a.systemPrompt
	if a.skillsBlock != "" {
		prompt += "\n\n" + a.skillsBlock
	}
	messages = append(messages, engine.ModelMessage{Role: "system", Content: prompt})

	// Message 2: Session-stable context — the environment block. Built once and
	// cached so it never changes across turns, keeping the prefix stable for
	// DeepSeek's cache.
	if a.stableSessionBlock == "" {
		a.stableSessionBlock = BuildStableSessionContext(a.envInfo)
		if a.agentsBlock != "" {
			a.stableSessionBlock += a.agentsBlock
		}
	}
	messages = append(messages, engine.ModelMessage{Role: "user", Content: a.stableSessionBlock})

	// === HISTORY ZONE (append-only — cacheable prefix) ===
	// History sits after the stable zone. Since previous turns' messages
	// never change, the entire history is a strict prefix extension and
	// benefits from DeepSeek's prefix cache — thousands of tokens per turn.
	for _, msg := range history {
		messages = append(messages, mapMessage(msg))
	}

	if len(toolResults) > 0 {
		for _, result := range toolResults {
			messages = append(messages, engine.ModelMessage{
				Role:       "tool",
				Content:    result.Digest,
				ToolCallID: result.ToolCallID,
			})
		}
	}

	return messages
}

func (a *ContextAssembler) EstimateTokens(messages []engine.ModelMessage) int {
	count := 0
	for _, msg := range messages {
		count += a.estimator.Estimate(msg.Content)
		count += a.estimator.Estimate(msg.ReasoningContent)
		if len(msg.ToolCalls) > 0 {
			for _, call := range msg.ToolCalls {
				count += a.estimator.Estimate(call.ID)
				count += a.estimator.Estimate(call.Function.Name)
				count += a.estimator.Estimate(call.Function.Arguments)
			}
		}
	}
	return count
}

// buildEnvironmentInfo assembles the session-stable environment data. The
// directory tree snapshot is generated from the project root (which the engine
// sets to the work dir), not from os.Getwd(), so it reflects the actual project
// even if the process CWD differs.
func buildEnvironmentInfo(root string) EnvironmentInfo {
	wd, err := os.Getwd()
	if err != nil {
		wd = ""
	}
	return EnvironmentInfo{
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		CWD:     wd,
		Date:    time.Now().Format("2006-01-02"),
		DirTree: buildDirTree(root, treeMaxDepth, treeMaxEntries),
	}
}

// hasFirstUserMessage was used to defer prompt building until the session
// language could be locked; the prompt is now language-independent, so it is
// gone.

func mapMessage(msg engine.Message) engine.ModelMessage {
	model := engine.ModelMessage{
		Role:             msg.Role,
		Content:          msg.Content,
		ToolCallID:       msg.ToolCallID,
		ReasoningContent: msg.ReasoningContent,
	}
	if len(msg.ToolCalls) > 0 {
		model.ToolCalls = make([]engine.ModelToolCall, 0, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			model.ToolCalls = append(model.ToolCalls, engine.ModelToolCall{
				ID:   call.ID,
				Type: "function",
				Function: engine.ModelFunctionCall{
					Name:      call.Name,
					Arguments: call.Arguments,
				},
			})
		}
	}
	return model
}
