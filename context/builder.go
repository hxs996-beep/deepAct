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
		envInfo:     buildEnvironmentInfo(projectRoot),
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
	// Once userLang is known, build the system prompt from the single canonical
	// prompt set. This is done once and cached, keeping the prefix stable across
	// turns. The prompt instructs the model to respond in the user's language,
	// so one set suffices regardless of session language.
	if a.userLangSet && a.userLang != "" && a.systemPromptWithLang == "" {
		lang := DetectLanguage(a.projectRoot)
		langPack := GetLangPack(lang, a.userLang)
		prompts := promptset.Get()
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
	// Active skill methodology lives HERE, after history, not in the stable zone.
	// Placing it in the stable zone (before history) meant activating or switching a
	// skill inserted/removed a message at that position, shifting the entire history
	// and invalidating the prefix cache for the session. As a tail message it is
	// still in the model's attention (recency — the end is attended best), and a
	// skill change only touches the tail, leaving the cached prefix intact. Only the
	// skill content changes; the standing "overrides general rules" marker below keeps
	// it authoritative, and the skillReminder in Block B keeps it in mind each turn.
	if a.activeSkillBlock != "" {
		messages = append(messages, engine.ModelMessage{Role: "user", Content: a.activeSkillBlock})
	}

	// Block B: runtime state as single compact JSON. Goal, open questions, recent
	// decisions, recent modified files, current step and counts live here — kept as
	// JSON (not prose) so the model treats it as reference data. The engine keeps the
	// full state in-process (read loop-guard, cross-session memory, and the final
	// completion summary) independent of what is rendered here.
	blockB := BuildBlockB(formatTaskStateVolatile(state), a.userLang)
	messages = append(messages, engine.ModelMessage{Role: "user", Content: blockB})

	// Analysis mode constraint: when the user's intent is analysis-only, inject
	// the constraint on every Build call so it persists across turns. The former
	// approach used pendingPinnedMessages which was cleared after the first turn.
	if state != nil && state.AnalysisMode {
		constraint := "[ANALYSIS MODE] 用户要求仅进行分析，不要修改任何代码。你的任务仅限于：阅读代码、分析原因、解释行为。禁止：edit、write、或任何修改文件的操作。"
		if a.userLang != "中文" {
			constraint = "[ANALYSIS MODE] The user asked for analysis only. Do NOT modify any code. Your task is limited to: reading code, analyzing causes, explaining behavior. FORBIDDEN: edit, write, or any file modification operations."
		}
		messages = append(messages, engine.ModelMessage{Role: "user", Content: constraint})
	}

	return messages
}

func (a *ContextAssembler) EstimateTokens(messages []engine.ModelMessage) int {
	count := 0
	for _, msg := range messages {
		count += a.estimator.Estimate(msg.Content)
		// reasoning_content is not counted: the wire request strips it (see
		// llm.stripReasoningContent), so it never contributes to the billed prompt.
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

// Live-state rendering budget. The rendered Block B is the model's "current
// reasoning state", so it must stay small and stable to keep the tail cache
// miss small and avoid leaking stale all-session data. These caps sit well
// above realistic single-task sizes (so nothing is lost mid-task) but bound a
// long multi-task session where only the recent window is useful. The ENGINE
// keeps the full state — read loop-guard, cross-session memory, and the final
// completion summary — independent of how much is rendered here.
const (
	maxRenderedDecisions = 30
	maxRenderedModified  = 20
	maxRenderedMarkers   = 20
)

func formatTaskStateVolatile(state *engine.TaskState) string {
	if state == nil {
		return ""
	}
	volatile := struct {
		Goal             string              `json:"goal,omitempty"`
		ActiveSkillName  string              `json:"active_skill_name,omitempty"`
		SkillReminder    string              `json:"skill_reminder,omitempty"`
		MemoryMarkers    []string            `json:"memory_markers,omitempty"`
		OpenQuestions    []string            `json:"open_questions,omitempty"`
		Assumptions      []string            `json:"assumptions,omitempty"`
		RecentDecisions  []decisionVolatile  `json:"recent_decisions,omitempty"`
		ModifiedCount    int                 `json:"modified_count"`
		RecentModified   []string            `json:"recent_modified,omitempty"`
		CurrentStep      string              `json:"current_step,omitempty"`
		TurnNumber       int                 `json:"turn_number"`
		ConsecutiveFails int                 `json:"consecutive_failures"`
		EditScopeFiles   int                 `json:"edit_scope_files"`
		Roundtable       *roundtableVolatile `json:"roundtable,omitempty"`
	}{
		Goal:             state.Goal,
		ActiveSkillName:  state.ActiveSkillName,
		SkillReminder:    skillReminder(state.ActiveSkillName),
		MemoryMarkers:    lastN(state.MemoryMarkers, maxRenderedMarkers),
		OpenQuestions:    state.OpenQuestions,
		Assumptions:      state.Assumptions,
		RecentDecisions:  lastNDecisions(state.Decisions, maxRenderedDecisions),
		ModifiedCount:    len(state.ModifiedFiles),
		RecentModified:   lastN(state.ModifiedFiles, maxRenderedModified),
		CurrentStep:      currentPlanStep(state.Plan),
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

// lastN returns the last n elements of in, or the whole slice when shorter.
// A nil slice stays nil so the field is omitted from the rendered JSON.
func lastN(in []string, n int) []string {
	if len(in) == 0 {
		return nil
	}
	if len(in) <= n {
		return in
	}
	return in[len(in)-n:]
}

// lastNDecisions projects the most recent n decisions into the compact form.
// Only the recent window matters for consistent reasoning; the full history is
// preserved by the session log and the cross-session memory store.
func lastNDecisions(in []engine.Decision, n int) []decisionVolatile {
	if len(in) == 0 {
		return nil
	}
	start := 0
	if len(in) > n {
		start = len(in) - n
	}
	out := make([]decisionVolatile, 0, len(in)-start)
	for _, d := range in[start:] {
		out = append(out, decisionVolatile{Text: d.Text})
	}
	return out
}

// decisionVolatile is the compact form of a Decision rendered into Block B.
type decisionVolatile struct {
	Text string `json:"text"`
}

// skillReminder returns a short reminder that the active skill's methodology
// is in the tail runtime context and must be followed. Included in Block B so
// the model sees it every turn without relying on distant history.
func skillReminder(name string) string {
	if name == "" {
		return ""
	}
	return fmt.Sprintf("⚠ Skill '%s' is ACTIVE. Its full methodology is in the [SKILL ACTIVE: %s] block near the end of the context. "+
		"It OVERRIDES general rules — follow it precisely step by step.", name, name)
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
	Phase        string   `json:"phase"`
	Goal         string   `json:"goal,omitempty"`
	DebateRounds int      `json:"debate_rounds,omitempty"`
	MemberIDs    []string `json:"member_ids,omitempty"`
}

// flattenRoundtable converts engine.RoundtableState to the compact volatile form.
func flattenRoundtable(rt *engine.RoundtableState) *roundtableVolatile {
	if rt == nil {
		return nil
	}
	v := &roundtableVolatile{
		Phase:        rt.Phase.String(),
		Goal:         truncString(rt.Goal, 120),
		DebateRounds: len(rt.DebateRounds),
	}
	if len(rt.Members) > 0 {
		v.MemberIDs = make([]string, len(rt.Members))
		for i, m := range rt.Members {
			v.MemberIDs[i] = m.ID
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
