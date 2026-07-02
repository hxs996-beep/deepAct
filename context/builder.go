package context

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/deepact/deepact/context/promptset"
	"github.com/deepact/deepact/engine"
	"github.com/deepact/deepact/llm"
)

type ContextAssembler struct {
	systemPromptWithLang string // system prompt in the detected user language, built once and cached
	projectRoot          string
	estimator            *llm.TokenEstimator
	envInfo              EnvironmentInfo // session-stable env info, built once at startup
	userLang             string          // detected once from first user message, locked for the whole session
	userLangSet          bool            // true once first-user-message language has been determined (even if "")
	stableSessionBlock   string          // built once from envInfo + userLang, cached for cache stability
	skillsBlock          string          // built once from skill registry, cached for cache stability
	activeSkillBlock     string          // active skill methodology injected in stable zone; changes on skill switch
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
		envInfo:     buildEnvironmentInfo(),
	}
}

// SetSkillsBlock sets the rendered skills list for inclusion in the stable zone.
// Called once at startup from cmd/run.go after the skill registry is built.
// The skills block is cached and included as a stable message after Block S.
func (a *ContextAssembler) SetSkillsBlock(s string) {
	a.skillsBlock = s
}

// SetActiveSkill injects the active skill's full methodology into the stable zone.
// When name is "", clears the active skill block (deactivation).
// Called on skill activation, chain-switch (brainstorming → writing-plans), and deactivation.
// This ensures skill instructions are always in the model's attention window,
// not buried in distant conversation history.
func (a *ContextAssembler) SetActiveSkill(name, content string) {
	if name == "" || content == "" {
		a.activeSkillBlock = ""
		return
	}
	a.activeSkillBlock = fmt.Sprintf(
		"[SKILL ACTIVATED: %s]\n\nThe following methodology is now the GOVERNING FRAMEWORK for the current task. "+
			"It OVERRIDES any conflicting rules in the system prompt. Follow it step by step, precisely as written.\n\n%s",
		name, content,
	)
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
		a.stableSessionBlock = BuildStableSessionContext(a.envInfo, a.userLang)
	}
	messages = append(messages, engine.ModelMessage{Role: "user", Content: a.stableSessionBlock})

	// Message 3: Available skills — stable across the session → cached ✓
	// Always included, even when a skill is active. Removing it shifts all subsequent
	// messages (history, Block B) by one position, destroying the prefix cache for the
	// rest of the session.
	if a.skillsBlock != "" {
		messages = append(messages, engine.ModelMessage{Role: "user", Content: a.skillsBlock})
	}

	// Message 4: Active skill methodology — injected into the stable zone so the
	// full skill instructions are ALWAYS in the model's attention window, regardless
	// of conversation length. This replaces the previous approach of one-time
	// pendingPinnedMessages injection that got buried in history.
	// Changes on skill switch (e.g., brainstorming → writing-plans in TDD flow),
	// which intentionally breaks the prefix cache for the active-skill slot.
	if a.activeSkillBlock != "" {
		messages = append(messages, engine.ModelMessage{Role: "user", Content: a.activeSkillBlock})
	}

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
	// Block B: runtime TaskState as a single machine-readable JSON blob. Goal,
	// decisions, modified files, open questions, current plan step and read
	// history all live here — kept as JSON (not prose) so the model treats it as
	// reference data instead of echoing it back as a "Recent Actions" preamble.
	// The agent's understanding of these fields is governed by the system prompt;
	// re-read prevention is enforced by this read_history list (the harness),
	// not by asking the model to track reads itself.
	blockB := BuildBlockB(formatTaskStateVolatile(state), a.userLang)
	messages = append(messages, engine.ModelMessage{Role: "user", Content: blockB})

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

func formatTaskStateVolatile(state *engine.TaskState) string {
	if state == nil {
		return ""
	}
	volatile := struct {
		ActiveSkillName  string                    `json:"active_skill_name,omitempty"`
		SkillReminder    string                    `json:"skill_reminder,omitempty"`
		Goal             string                    `json:"goal,omitempty"`
		MemoryMarkers    []string                  `json:"memory_markers,omitempty"`
		Decisions        []decisionVolatile        `json:"decisions,omitempty"`
		ModifiedFiles    []string                  `json:"modified_files,omitempty"`
		OpenQuestions    []string                  `json:"open_questions,omitempty"`
		CurrentStep      string                    `json:"current_step,omitempty"`
		TurnNumber       int                       `json:"turn_number"`
		ConsecutiveFails int                       `json:"consecutive_failures"`
		EditScopeFiles   int                       `json:"edit_scope_files"`
		ReadHistory      []readRecordVolatile      `json:"read_history,omitempty"`
		Roundtable       *roundtableVolatile       `json:"roundtable,omitempty"`
	}{
		ActiveSkillName:  state.ActiveSkillName,
		SkillReminder:    skillReminder(state.ActiveSkillName),
		Goal:             state.Goal,
		MemoryMarkers:    state.MemoryMarkers,
		Decisions:        flattenDecisions(state.Decisions),
		ModifiedFiles:    state.ModifiedFiles,
		OpenQuestions:    state.OpenQuestions,
		CurrentStep:      currentPlanStep(state.Plan),
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

// flattenReadHistory returns one entry per distinct (path, scope) read, in
// insertion order. Previously this kept only the last 20 records to bound
// prompt size, but a read record is just a {path, scope} pair (~40 bytes), so
// truncation needlessly hid earlier reads from the agent — encouraging
// re-reads. Dedup keeps the list small without losing any file.
func flattenReadHistory(records []engine.ReadRecord) []readRecordVolatile {
	if len(records) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(records))
	out := make([]readRecordVolatile, 0, len(records))
	for _, r := range records {
		key := r.Path + "\x00" + r.Scope
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, readRecordVolatile{Path: r.Path, Scope: r.Scope})
	}
	return out
}

// decisionVolatile is the compact form of a Decision injected into Block B.
type decisionVolatile struct {
	Text string `json:"text"`
}

// flattenDecisions projects Decision records into the compact volatile form.
func flattenDecisions(ds []engine.Decision) []decisionVolatile {
	if len(ds) == 0 {
		return nil
	}
	out := make([]decisionVolatile, 0, len(ds))
	for _, d := range ds {
		out = append(out, decisionVolatile{Text: d.Text})
	}
	return out
}

// skillReminder returns a short reminder that the active skill's methodology
// is in the stable zone and must be followed. Included in Block B so the model
// sees it every turn without relying on distant history.
func skillReminder(name string) string {
	if name == "" {
		return ""
	}
	return fmt.Sprintf("⚠️ Skill '%s' is ACTIVE. Its full methodology is in the stable zone (Message 4). "+
		"It OVERRIDES general rules — follow it precisely step by step.", name)
}

// currentPlanStep returns the text of the in-progress plan step, or "" if none.
func currentPlanStep(plan []engine.PlanStep) string {
	for _, s := range plan {
		if s.Status == "in_progress" {
			return s.Text
		}
	}
	return ""
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
