package context

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/deepact/deepact/context/promptset"
	"github.com/deepact/deepact/engine"
	"github.com/deepact/deepact/llm"
)

type ContextAssembler struct {
	systemPromptWithLang  string // system prompt in the detected user language, built once and cached
	projectRoot           string
	estimator             *llm.TokenEstimator
	deepactMD             string // cached deepact.md content, read once at startup
	envInfo               EnvironmentInfo // session-stable env info, built once at startup
	userLang              string          // detected once from first user message, locked for the whole session
	userLangSet           bool            // true once first-user-message language has been determined (even if "")
	stableSessionBlock    string          // built once from deepactMD + envInfo + userLang, cached for cache stability
	skillsBlock           string          // built once from skill registry, cached for cache stability
}

func NewContextAssembler(projectRoot string, estimator *llm.TokenEstimator) *ContextAssembler {
	// systemPrompt is built lazily once userLang is known (see Build).
	// We still detect the project language for langPack selection.
	_ = DetectLanguage(projectRoot)

	if estimator == nil {
		estimator = llm.NewTokenEstimator()
	}

	return &ContextAssembler{
		projectRoot: projectRoot,
		estimator:   estimator,
		deepactMD:   readDeepactMD(),
		envInfo:     buildEnvironmentInfo(),
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
	// Built once from the detected user language's prompt set (see below).
	prompt := a.systemPromptWithLang
	if prompt == "" {
		prompt = "(loading...)"
	}
	messages = append(messages, engine.ModelMessage{Role: "system", Content: prompt})

	// Message 2: Session-stable context — language detected from the FIRST
	// user message and locked for the session (see detectUserLanguage). We only
	// resolve it once the first real user message is present, then cache both
	// the language and the assembled block so they never change again — this
	// keeps the prefix stable for DeepSeek's cache AND prevents later English
	// confirmations ("ok") from flipping the locked language.
	if !a.userLangSet && hasFirstUserMessage(history) {
		a.userLang = detectUserLanguage(history)
		a.userLangSet = true
	}
	// Once userLang is known, build the system prompt from the appropriate
	// language's prompt set. This is done once and cached, keeping the prefix
	// stable across turns. No separate language directive is needed — the
	// prompt itself is already in the correct language.
	if a.userLangSet && a.userLang != "" && a.systemPromptWithLang == "" {
		lang := DetectLanguage(a.projectRoot)
		langPack := GetLangPack(lang, a.userLang)
		prompts := promptset.Get(a.userLang)
		a.systemPromptWithLang = prompts.System + "\n\n# Language Pack\n" + langPack + "\n\n" + prompts.Examples
	}
	if a.stableSessionBlock == "" && a.userLangSet {
		a.stableSessionBlock = BuildStableSessionContext(a.deepactMD, a.envInfo, a.userLang)
	}
	messages = append(messages, engine.ModelMessage{Role: "user", Content: a.stableSessionBlock})

	// Message 3: Available skills — stable across the session → cached ✓
	// Always included, even when a skill is active. Removing it shifts all subsequent
	// messages (history, Block B) by one position, destroying the prefix cache for the
	// rest of the session. The active skill's methodology is enforced via the
	// [SKILL ACTIVATED] pinned message, not by hiding the skills list.
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
	// Block B: minimal task state fields only + language directive at end
	blockB := BuildBlockB(formatTaskStateVolatile(state), a.userLang)
	messages = append(messages, engine.ModelMessage{Role: "user", Content: blockB})

	reminder := BuildTaskReminder(state, a.userLang)
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

// hasFirstUserMessage reports whether history contains at least one non-empty
// user message. Used to defer language detection until the first real user
// turn arrives (the engine may call Build before any user input).
func hasFirstUserMessage(history []engine.Message) bool {
	for _, m := range history {
		if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
			return true
		}
	}
	return false
}

func readDeepactMD() string {
	home, _ := os.UserHomeDir()
	paths := []string{
		"/etc/deepact/deepact.md",
		filepath.Join(home, ".deepact", "deepact.md"),
		filepath.Join(".deepact", "deepact.md"),
		"deepact.md",
		filepath.Join(home, ".deepact", "rules.md"),
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
