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
	envInfo            EnvironmentInfo // session-stable env info, built once at startup
	userLang           string          // detected once from first history, cached for cache stability
	stableSessionBlock string          // built once from agentsMD + envInfo + userLang, cached for cache stability
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
		envInfo:      buildEnvironmentInfo(),
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
		a.stableSessionBlock = BuildStableSessionContext(a.agentsMD, a.envInfo, a.userLang)
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

	// === HISTORY ZONE (append-only — cacheable prefix) ===
	// History is placed BEFORE AccumulatedBlocks to maximize prefix cache hits.
	// Since previous turns' messages never change, placing history here lets
	// the cache cover all past conversation (assistant reasoning + tool results
	// containing file contents from reads/greps) — thousands of tokens per turn.
	// Only the growing AccumulatedBlocks + volatile tail shift each turn,
	// keeping the cache-miss portion small (~500 tokens) regardless of turn count.
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

	// === ACCUMULATED BLOCKS (small, in volatile tail zone) ===
	// Turn summary blocks are only ~50-200 tokens each. They sit after history
	// in the non-cached tail, which is acceptable since the history cache gains
	// vastly outweigh the loss of blocks moving out of the prefix.
	if state != nil && len(state.AccumulatedBlocks) > 0 {
		for _, block := range state.AccumulatedBlocks {
			if block != "" {
				messages = append(messages, engine.ModelMessage{
					Role:    "user",
					Content: block,
				})
			}
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

// FormatTurnBlock builds a concise structured block string for a completed turn.
// The block is stored in TaskState.AccumulatedBlocks and rendered as a stable
// prefix message in subsequent turns.
func FormatTurnBlock(turnNum int, filesRead []string, filesSearched []string, filesModified []string, markers []string, decisions []string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Turn %d\n", turnNum))

	hasContent := false

	if len(filesRead) > 0 {
		b.WriteString("Read: " + strings.Join(filesRead, ", ") + "\n")
		hasContent = true
	}
	if len(filesSearched) > 0 {
		b.WriteString("Searched: " + strings.Join(filesSearched, ", ") + "\n")
		hasContent = true
	}
	if len(filesModified) > 0 {
		b.WriteString("Modified: " + strings.Join(filesModified, ", ") + "\n")
		hasContent = true
	}
	if len(markers) > 0 {
		b.WriteString("Findings:\n")
		for _, m := range markers {
			b.WriteString("  - " + m + "\n")
		}
		hasContent = true
	}
	if len(decisions) > 0 {
		b.WriteString("Decisions:\n")
		for _, d := range decisions {
			b.WriteString("  - " + d + "\n")
		}
		hasContent = true
	}

	if !hasContent {
		return "" // skip empty blocks
	}
	return b.String()
}

// formatTaskStateVolatile returns JSON for the minimal volatile TaskState fields
// that the model needs for decision-making but aren't available elsewhere.
// Redundant fields (covered by turn-blocks / reminder) are omitted to keep the
// cache-missing tail as small as possible.
func formatTaskStateVolatile(state *engine.TaskState) string {
	if state == nil {
		return ""
	}
	volatile := struct {
		TurnNumber       int                     `json:"turn_number"`
		ConsecutiveFails int                     `json:"consecutive_failures"`
		EditScopeFiles   int                     `json:"edit_scope_files"`
		Conference       *engine.ConferenceState `json:"conference,omitempty"`
		ParentContext    *engine.ParentBoard     `json:"parent_context,omitempty"`
	}{
		TurnNumber:       state.TurnNumber,
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
