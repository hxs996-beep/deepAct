package context

import (
	"encoding/json"
	"fmt"
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
	repoMap            *RepoMap
	projectRoot        string
	estimator          *llm.TokenEstimator
	agentsMD           string // cached AGENTS.md content, read once at startup
	userLang           string // detected once from first history, cached for cache stability
	stableSessionBlock string // built once from agentsMD + userLang, cached for cache stability
}

func NewContextAssembler(projectRoot string, estimator *llm.TokenEstimator) *ContextAssembler {
	lang := DetectLanguage(projectRoot)
	langPack := GetLangPack(lang)
	systemPrompt := SystemPromptBlockA + "\n\n# Language Pack\n" + langPack + "\n\n" + ExamplesBlock

	var rm *RepoMap
	if projectRoot != "" {
		rm, _ = GenerateRepoMap(projectRoot)
	}

	if estimator == nil {
		estimator = llm.NewTokenEstimator()
	}

	return &ContextAssembler{
		langPack:     langPack,
		examples:     ExamplesBlock,
		systemPrompt: systemPrompt,
		repoMap:      rm,
		projectRoot:  projectRoot,
		estimator:    estimator,
		agentsMD:     readAgentsMD(),
	}
}

func (a *ContextAssembler) RefreshRepoMap() {
	if a.projectRoot != "" {
		a.repoMap, _ = GenerateRepoMap(a.projectRoot)
	}
}

// RepoMapContent returns the rendered RepoMap string, or "" if none.
func (a *ContextAssembler) RepoMapContent() string {
	if a.repoMap != nil {
		return a.repoMap.Render()
	}
	return ""
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
		a.stableSessionBlock = BuildStableSessionContext(a.agentsMD, a.userLang)
	}
	messages = append(messages, engine.ModelMessage{Role: "user", Content: a.stableSessionBlock})

	// Message 3: Repo map — stable for the session → cached ✓
	if a.repoMap != nil {
		mapContent := a.repoMap.Render()
		if mapContent != "" {
			messages = append(messages, engine.ModelMessage{
				Role:    "user",
				Content: "[REPO MAP — use this to locate code before reading files]\n" + mapContent,
			})
		}
	}

	// === SEMI-STABLE ZONE (changes slowly — extends prefix cache) ===
	// Message 4: TaskState stable fields only (goal, decisions, plan, etc.)
	// These change infrequently so this message stays cached across many turns.
	if state != nil {
		stableTask := formatTaskStateStable(state)
		if stableTask != "" {
			messages = append(messages, engine.ModelMessage{
				Role:    "user",
				Content: "# Task Context (Stable)\n" + stableTask,
			})
		}
	}

	// === VARIABLE ZONE (changes each turn — cache miss acceptable) ===
	// Block B: env info + volatile task state fields only
	blockB := BuildBlockB(buildEnvironmentInfo(), formatTaskStateVolatile(state))
	messages = append(messages, engine.ModelMessage{Role: "user", Content: blockB})

	// History (older turns in middle, recent turns at bottom)
	freshStart := len(history) - engine.FreshTurns*3
	if freshStart < 0 {
		freshStart = 0
	}

	for i, msg := range history {
		if i < freshStart {
			messages = append(messages, mapMessage(msg))
		}
	}

	// === BOTTOM ZONE (highest recency attention) ===
	for i, msg := range history {
		if i >= freshStart {
			messages = append(messages, mapMessage(msg))
		}
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
		OS:    runtime.GOOS,
		Arch:  runtime.GOARCH,
		CWD:   wd,
		Model: "",
		Date:  time.Now().Format("2006-01-02"),
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

func formatTaskState(state *engine.TaskState) string {
	if state == nil {
		return ""
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Sprintf("TaskState marshal error: %v", err)
	}
	return string(data)
}

// formatTaskStateStable returns JSON for slowly-changing TaskState fields.
// These go into a separate stable message before the variable BlockB,
// extending the prefix cache window across many turns.
func formatTaskStateStable(state *engine.TaskState) string {
	if state == nil {
		return ""
	}
	stable := struct {
		TaskID        string              `json:"task_id,omitempty"`
		Goal          string              `json:"goal,omitempty"`
		Confirmed     bool                `json:"confirmed_scope"`
		Constraints   []string            `json:"constraints,omitempty"`
		Assumptions   []string            `json:"assumptions,omitempty"`
		Decisions     []engine.Decision   `json:"decisions,omitempty"`
		MemoryMarkers []string            `json:"memory_markers,omitempty"`
		Plan          []engine.PlanStep   `json:"plan,omitempty"`
		OpenQuestions []string            `json:"open_questions,omitempty"`
	}{
		TaskID:        state.TaskID,
		Goal:          state.Goal,
		Confirmed:     state.ConfirmedScope,
		Constraints:   state.Constraints,
		Assumptions:   state.Assumptions,
		Decisions:     state.Decisions,
		MemoryMarkers: state.MemoryMarkers,
		Plan:          state.Plan,
		OpenQuestions: state.OpenQuestions,
	}
	data, err := json.Marshal(stable)
	if err != nil {
		return ""
	}
	return string(data)
}

// formatTaskStateVolatile returns JSON for frequently-changing TaskState fields.
// These stay in BlockB which changes every turn (cache miss expected).
func formatTaskStateVolatile(state *engine.TaskState) string {
	if state == nil {
		return ""
	}
	volatile := struct {
		TurnNumber        int                       `json:"turn_number"`
		WorkingSet        engine.WorkingSet         `json:"working_set,omitempty"`
		ModifiedFiles     []string                  `json:"modified_files,omitempty"`
		FileCollapse      []engine.FileCollapse     `json:"file_collapse,omitempty"`
		CallChain         []engine.CallChainEntry   `json:"call_chain,omitempty"`
		ConsecutiveFails  int                       `json:"consecutive_failures"`
		EditScopeFiles    int                       `json:"edit_scope_files"`
		Conference        *engine.ConferenceState   `json:"conference,omitempty"`
		ParentContext     *engine.ParentBoard       `json:"parent_context,omitempty"`
	}{
		TurnNumber:       state.TurnNumber,
		WorkingSet:       state.WorkingSet,
		ModifiedFiles:    state.ModifiedFiles,
		FileCollapse:     state.FileCollapse,
		CallChain:        state.CallChain,
		ConsecutiveFails: state.ConsecutiveFailures,
		EditScopeFiles:   state.EditScopeFiles,
		Conference:       state.Conference,
		ParentContext:    state.ParentContext,
	}
	data, err := json.Marshal(volatile)
	if err != nil {
		return ""
	}
	return string(data)
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
