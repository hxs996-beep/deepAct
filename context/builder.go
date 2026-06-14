package context

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/deepact/deepact/engine"
	"github.com/deepact/deepact/llm"
)

type ContextAssembler struct {
	langPack           string
	examples           string
	systemPrompt       string
	projectRoot        string
	estimator          *llm.TokenEstimator
	agentsMD           string // cached AGENTS.md content, read once at startup
	envInfo            EnvironmentInfo // session-stable env info, built once at startup
	userLang           string          // detected once from first history, cached for cache stability
	stableSessionBlock string          // built once from agentsMD + envInfo + userLang, cached for cache stability
	skillsBlock        string          // built once from skill registry, cached for cache stability
}

func NewContextAssembler(projectRoot string, estimator *llm.TokenEstimator) *ContextAssembler {
	lang := DetectLanguage(projectRoot)
	langPack := GetLangPack(lang)
	systemPrompt := SystemPromptBlockA + "\n\n# Language Pack\n" + langPack + "\n\n" + ExamplesBlock

	if estimator == nil {
		estimator = llm.NewTokenEstimator()
	}

	return &ContextAssembler{
		langPack:     langPack,
		examples:     ExamplesBlock,
		systemPrompt: systemPrompt,
		projectRoot:  projectRoot,
		estimator:    estimator,
		agentsMD:     readAgentsMD(),
		envInfo:      buildEnvironmentInfo(),
	}
}

// SetSkillsBlock sets the rendered skills list for inclusion in the stable zone.
// Called once at startup from cmd/run.go after the skill registry is built.
// The skills block is cached and included as a stable message after Block S.
func (a *ContextAssembler) SetSkillsBlock(s string) {
	a.skillsBlock = s
}

func (a *ContextAssembler) Build(state *engine.TaskState, history []engine.Message, toolResults []engine.ToolResult) []engine.ModelMessage {
	messages := make([]engine.ModelMessage, 0, len(history)+6)

	// === STABLE ZONE (TOP — prefix cache friendly) ===
	// Message 1: System prompt — identical every turn → cached ✓
	messages = append(messages, engine.ModelMessage{Role: "system", Content: a.systemPrompt})

	// Message 2: Session-stable context — detected once, cached → cached ✓
	if a.userLang == "" {
		a.userLang = detectUserLanguage(history)
	}
	if a.stableSessionBlock == "" {
		a.stableSessionBlock = BuildStableSessionContext(a.agentsMD, a.envInfo, a.userLang)
	}
	messages = append(messages, engine.ModelMessage{Role: "user", Content: a.stableSessionBlock})

	// Message 3: Available skills — stable across the session → cached ✓
	if a.skillsBlock != "" {
		messages = append(messages, engine.ModelMessage{Role: "user", Content: a.skillsBlock})
	}

	// Message 4: (reserved for future use — was RepoMap)

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

	// === VOLATILE TAIL (small, changes each turn — cache miss acceptable) ===
	// Block B: minimal task state fields only
	blockB := BuildBlockB(formatTaskStateVolatile(state))
	messages = append(messages, engine.ModelMessage{Role: "user", Content: blockB})

	reminder := BuildTaskReminder(state)
	if reminder != "" {
		messages = append(messages, engine.ModelMessage{Role: "system", Content: reminder})
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

func buildEnvironmentInfo() EnvironmentInfo {
	wd, err := os.Getwd()
	if err != nil {
		wd = ""
	}
	return EnvironmentInfo{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
		CWD:  wd,
		Date: time.Now().Format("2006-01-02"),
	}
}

func readAgentsMD() string {
	home, _ := os.UserHomeDir()
	paths := []string{
		"/etc/deepact/AGENTS.md",
		filepath.Join(home, ".deepact", "AGENTS.md"),
		filepath.Join(".deepact", "AGENTS.md"),
		"AGENTS.md",
		filepath.Join(".deepact", "AGENTS.override.md"),
	}

	var parts []string
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		parts = append(parts, content)
	}

	return strings.Join(parts, "\n\n---\n\n")
}

func formatTaskStateVolatile(state *engine.TaskState) string {
	if state == nil {
		return ""
	}
	volatile := struct {
		ActiveSkillName  string                    `json:"active_skill_name,omitempty"`
		TurnNumber       int                       `json:"turn_number"`
		ConsecutiveFails int                       `json:"consecutive_failures"`
		EditScopeFiles   int                       `json:"edit_scope_files"`
		Roundtable       *roundtableVolatile       `json:"roundtable,omitempty"`
	}{
		ActiveSkillName:  state.ActiveSkillName,
		TurnNumber:       state.TurnNumber,
		ConsecutiveFails: state.ConsecutiveFailures,
		EditScopeFiles:   state.EditScopeFiles,
		Roundtable:       flattenRoundtable(state.Roundtable),
	}
	data, err := json.Marshal(volatile)
	if err != nil {
		return ""
	}
	return string(data)
}

// roundtableVolatile is a compact representation of roundtable results
// injected into Block B for the main agent to make informed decisions.
type roundtableVolatile struct {
	Phase      string              `json:"phase"`
	Goal       string              `json:"goal,omitempty"`
	ChosenPlan string              `json:"chosen_plan,omitempty"`
	Reviews    []reviewSummary     `json:"reviews,omitempty"`
}

type reviewSummary struct {
	Member  string `json:"member"`
	Score   int    `json:"score"`
	Verdict string `json:"verdict"`
	Summary string `json:"summary,omitempty"`
}

// flattenRoundtable converts engine.RoundtableState to the compact volatile form.
// Only includes Reviews when the roundtable is done and reviews exist.
func flattenRoundtable(rt *engine.RoundtableState) *roundtableVolatile {
	if rt == nil {
		return nil
	}
	v := &roundtableVolatile{
		Phase:      rt.Phase.String(),
		Goal:       truncString(rt.Goal, 120),
		ChosenPlan: truncString(rt.ChosenPlan, 120),
	}
	if len(rt.Reviews) > 0 {
		v.Reviews = make([]reviewSummary, 0, len(rt.Reviews))
		for _, r := range rt.Reviews {
			v.Reviews = append(v.Reviews, reviewSummary{
				Member:  r.MemberID,
				Score:   r.Score,
				Verdict: string(r.Verdict),
				Summary: truncString(r.Summary, 150),
			})
		}
	}
	return v
}

func truncString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

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
