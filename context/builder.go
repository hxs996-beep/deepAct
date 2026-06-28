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
	deepactMD          string // cached deepact.md content, read once at startup
	envInfo            EnvironmentInfo // session-stable env info, built once at startup
	userLang           string          // detected once from first history, cached for cache stability
	stableSessionBlock string          // built once from deepactMD + envInfo + userLang, cached for cache stability
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
		deepactMD:    readDeepactMD(),
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
		a.stableSessionBlock = BuildStableSessionContext(a.deepactMD, a.envInfo, a.userLang)
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

	// Read-history hint: warn the agent against re-reading files already read.
	if hint := BuildReadHistoryHint(state.ReadHistory, a.userLang); hint != "" {
		messages = append(messages, engine.ModelMessage{Role: "system", Content: hint})
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

func readDeepactMD() string {
	home, _ := os.UserHomeDir()
	paths := []string{
		"/etc/deepact/deepact.md",
		filepath.Join(home, ".deepact", "deepact.md"),
		filepath.Join(".deepact", "deepact.md"),
		"deepact.md",
		filepath.Join(".deepact", "deepact.override.md"),
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
		ReadHistory      []readRecordVolatile      `json:"read_history,omitempty"`
		Roundtable       *roundtableVolatile       `json:"roundtable,omitempty"`
	}{
		ActiveSkillName:  state.ActiveSkillName,
		TurnNumber:       state.TurnNumber,
		ConsecutiveFails: state.ConsecutiveFailures,
		EditScopeFiles:   state.EditScopeFiles,
		ReadHistory:      flattenReadHistory(state.ReadHistory),
		Roundtable:       flattenRoundtable(state.Roundtable),
	}
	data, err := json.Marshal(volatile)
	if err != nil {
		return ""
	}
	return string(data)
}

// readRecordVolatile is the compact form of a ReadRecord injected into Block B.
type readRecordVolatile struct {
	Path  string `json:"path"`
	Scope string `json:"scope,omitempty"`
}

// flattenReadHistory keeps the last 20 read records to bound prompt size; the
// most recent reads matter most for the agent's current reasoning.
func flattenReadHistory(records []engine.ReadRecord) []readRecordVolatile {
	if len(records) == 0 {
		return nil
	}
	start := 0
	if len(records) > 20 {
		start = len(records) - 20
	}
	out := make([]readRecordVolatile, 0, len(records)-start)
	for _, r := range records[start:] {
		out = append(out, readRecordVolatile{Path: r.Path, Scope: r.Scope})
	}
	return out
}

// BuildReadHistoryHint renders a system message listing already-read files
// (aggregated by path) so the agent avoids re-reading them. Returns "" when
// there is nothing to show.
func BuildReadHistoryHint(records []engine.ReadRecord, lang string) string {
	if len(records) == 0 {
		return ""
	}
	zh := lang == "zh" || lang == "chinese"
	byPath := make(map[string][]string)
	order := []string{}
	for _, r := range records {
		if _, ok := byPath[r.Path]; !ok {
			order = append(order, r.Path)
		}
		byPath[r.Path] = append(byPath[r.Path], describeScopeForHint(r.Scope, zh))
	}
	var sb strings.Builder
	if zh {
		sb.WriteString("已读文件（内容已在对话历史中，不要重读）：\n")
	} else {
		sb.WriteString("Files already read (content is in conversation history — do not re-read):\n")
	}
	for _, p := range order {
		seen := map[string]bool{}
		uniq := []string{}
		for _, s := range byPath[p] {
			if !seen[s] {
				seen[s] = true
				uniq = append(uniq, s)
			}
		}
		joined := strings.Join(uniq, ", ")
		if zh {
			sb.WriteString("- " + p + "（" + joined + "）\n")
		} else {
			sb.WriteString("- " + p + " (" + joined + ")\n")
		}
	}
	if zh {
		sb.WriteString("需要新信息时：用 lsp 或读取该文件尚未读过的区段。")
	} else {
		sb.WriteString("For new info: use lsp or read an un-read section of the file.")
	}
	return sb.String()
}

// describeScopeForHint renders a scope string for the read-history hint.
func describeScopeForHint(scope string, zh bool) string {
	if scope == "" {
		if zh {
			return "全文"
		}
		return "full"
	}
	if strings.HasPrefix(scope, "symbol:") {
		return scope
	}
	if zh {
		return "行 " + scope
	}
	return "L " + scope
}

// roundtableVolatile is a compact representation of roundtable results
// injected into Block B for the main agent to make informed decisions.
type roundtableVolatile struct {
	Phase   string          `json:"phase"`
	Goal    string          `json:"goal,omitempty"`
	Reviews []reviewSummary `json:"reviews,omitempty"`
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
		Phase: rt.Phase.String(),
		Goal:  truncString(rt.Goal, 120),
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
