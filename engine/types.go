package engine

import (
	"encoding/json"
	"time"
)

// UserIntent classifies the user's intention for the current message,
// used to control PlanConfirmed reset and analysis-only constraints.
type UserIntent int

const (
	IntentContinue UserIntent = iota // continuing previous task — keep PlanConfirmed
	IntentNewTopic                   // new topic, different from previous goal — reset PlanConfirmed
	IntentAnalyze                    // analysis/explanation only, no modifications — reset + inject constraint
)

type Stage int

const (
	StageIntake Stage = iota
	StagePlan
	StageDesignGuard
	StageAct
	StageVerifyFailed // critic adversarial verification returned FAIL — user must decide
	StageVerifyCompact
)

type CompressionLayer int

const (
	LayerToolGovernance CompressionLayer = iota
	LayerFullCompact
)

type ProgressEvent struct {
	Type       string // "tool_start" | "tool_done" | "thinking" | "content_delta" | "reasoning_delta" | "agent_start" | "agent_done" | "usage"
	Name       string
	Detail     string // brief digest for live display
	FullDetail string // full content (e.g., diff) for final rendering
	Usage      *ModelUsage
	ModelName  string // which model was used for this API call
}

type ProgressFunc func(event ProgressEvent)

// ModelPricing defines per-token pricing for a model, in RMB.
type ModelPricing struct {
	InputPricePerToken         float64 // e.g. 0.000003 for ¥3/1M tokens
	OutputPricePerToken        float64 // e.g. 0.000006 for ¥6/1M tokens
	CacheHitInputPricePerToken float64 // e.g. 0.000000025 for ¥0.025/1M tokens (separate from input)
}

// PricingConfig maps model names to their pricing.
// If a model is not found in Models, Default is used.
type PricingConfig struct {
	Models  map[string]ModelPricing
	Default ModelPricing
}

type EngineConfig struct {
	SessionID              string
	ModelName              string // default (Pro) model name
	FlashModelName         string // Flash model name for cheaper agents
	BaseURL                string // API base URL (e.g. https://api.deepseek.com or https://openrouter.ai/api/v1)
	SubAgentBaseURL        string // separate API base URL for sub-agents (cache isolation); empty = same as BaseURL
	MaxTurns               int
	MaxIterationsPerTurn   int
	MaxContextTokens       int
	// MaxOutputTokens caps the LLM completion length per turn (max_tokens).
	// DeepSeek's 1M context window supports large completions; a generous
	// budget lets the model emit full code edits in one turn. 0 = use the
	// DefaultMaxOutputTokens const.
	MaxOutputTokens        int
	PlanningEnabled        bool
	PlanningThresholdChars int
	AutoConfirmScope       bool
	ShowThinking           bool   // stream model reasoning/thinking to UI
	RiskThreshold          float64 // router risk threshold for Pro/Flash escalation
	ToolAllowList          []string
	WorkDir                string
	OnProgress             ProgressFunc
	Pricing                PricingConfig
	EvalStoreDir           string // directory for evaluation records JSONL (default: ~/.deepact/eval/)
	PromptVersion          string // SHA256 hash of the system prompt for tracking
	// TeamMembers is the ordered list of member IDs to use in /team debate mode.
	// Empty = use DefaultDebateMembers.
	TeamMembers []string
}

type EngineResponse struct {
	Summary      string   `json:"summary"`
	Questions    []string `json:"questions,omitempty"`
	Options      []string `json:"options,omitempty"` // e.g. ["方案A: 用Redis", "方案B: 用MySQL"]
	NextStep     string   `json:"next_step,omitempty"`
	Stage        Stage    `json:"stage"`
	Blocked      bool     `json:"blocked"`
	BlockedBy    string   `json:"blocked_by,omitempty"`
	FinishReason string   `json:"finish_reason,omitempty"`
}

type ModelRequest struct {
	Model           string
	Messages        []ModelMessage
	Tools           []ModelTool
	Temperature     float64
	MaxTokens       int
	ReasoningEffort string
	JsonMode        bool
	ThinkingEnabled bool
}

type ModelMessage struct {
	Role             string
	Content          string
	ToolCalls        []ModelToolCall
	ToolCallID       string
	ReasoningContent string
}

type ModelTool struct {
	Type     string
	Function ModelToolFunction
}

type ModelToolFunction struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type ModelToolCall struct {
	ID       string
	Type     string
	Function ModelFunctionCall
}

type ModelFunctionCall struct {
	Name      string
	Arguments string
}

type ModelResponse struct {
	ID               string
	Model            string
	Message          ModelMessage
	FinishReason     string
	Usage            ModelUsage
	ReasoningContent string
}

type ModelUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CacheHitTokens   int
	CacheMissTokens  int
}

type ModelChunk struct {
	Delta          string
	ReasoningDelta string
	ToolCalls      []ModelToolCall
	FinishReason   string
	Usage          *ModelUsage
	Err            error
	RetryProgress  string // non-empty when a retry is about to start
}

type ToolExecContext struct {
	WorkDir    string
	SessionID  string
	TurnNumber int
}

type ToolCallRequest struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type ToolResult struct {
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	Status      string `json:"status"`
	Digest      string `json:"digest"`
	ArtifactRef string `json:"artifact_ref,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
}

type Event struct {
	SessionID string          `json:"session_id"`
	Type      string          `json:"type"`
	Stage     Stage           `json:"stage"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type Message struct {
	Role             string            `json:"role"`
	Content          string            `json:"content,omitempty"`
	ToolCalls        []MessageToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	Timestamp        time.Time         `json:"timestamp"`
}

type MessageToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type TaskState struct {
	TaskID              string           `json:"task_id"`
	Goal                string           `json:"goal"`
	ConfirmedScope      bool             `json:"confirmed_scope"`
	Constraints         []string         `json:"constraints"`
	Assumptions         []string         `json:"assumptions"`
	Decisions           []Decision       `json:"decisions"`
	MemoryMarkers       []string         `json:"memory_markers"` // extracted from <!-- REMEMBER: ... --> in model output
	Plan                []PlanStep       `json:"plan"`
	WorkingSet          WorkingSet       `json:"working_set"`
	OpenQuestions       []string         `json:"open_questions"`
	ModifiedFiles       []string         `json:"modified_files"`
	FileCollapse        []FileCollapse   `json:"file_collapse"`
	CallChain           []CallChainEntry `json:"call_chain"`
	TurnNumber          int              `json:"turn_number"`
	ConsecutiveFailures int              `json:"consecutive_failures"`
	EditScopeFiles      int              `json:"edit_scope_files"`
	PlanConfirmed       bool             `json:"plan_confirmed"`                       // user approved the edit plan, skip per-edit guard
	PendingDangerousCmd string           `json:"pending_dangerous_cmd,omitempty"` // normalized command awaiting user confirmation
	PendingActivateSkill string           `json:"pending_activate_skill,omitempty"` // skill name awaiting user confirmation via activate_skill tool
	ActiveSkillName     string           `json:"active_skill_name,omitempty"`  // name of the currently activated skill
	ActiveSkillContent  string           `json:"active_skill_content,omitempty"` // full content of the activated skill
	SkillGatePassed     bool             `json:"skill_gate_passed,omitempty"`    // active skill's pre-implementation gate has been passed (user approval or NextSkills transition), allowing edits
	Roundtable          *RoundtableState `json:"roundtable,omitempty"`

	// ReadHistory records each file read this session (path + scope) so the
	// prompt can warn the agent against re-reading, and the loop guard can count
	// repeated reads of the same (path, scope). Cleared on new user message.
	ReadHistory []ReadRecord `json:"read_history"`

	// AnalysisMode is set when the user's intent is analysis-only. When true,
	// the context builder injects a [ANALYSIS MODE] constraint every turn,
	// persisting across turns (unlike the former pendingPinnedMessages approach
	// which was cleared after the first turn). Cleared when the user confirms
	// the analysis report or starts a new topic.
	AnalysisMode bool `json:"analysis_mode,omitempty"`

	// AnalysisReportConfirmed is set when the user confirms the analysis report
	// presented by the agent. When true, the analysis report gate is skipped,
	// allowing the edit plan guard to proceed normally.
	//
	// Scoped to a single Run: it is reset to false at the start of every Run
	// and only re-set within that Run by handleAnalysisNudgeConfirmation. This
	// prevents a confirmation from a prior task from leaking into an unrelated
	// new question (which made the agent skip presenting a fresh report and
	// falsely claim "analysis report already confirmed"). Later Runs rely on
	// pendingEditPlan / PlanConfirmed to skip the gate instead.
	AnalysisReportConfirmed bool `json:"analysis_report_confirmed,omitempty"`
}

// ReadRecord captures a single read operation for loop-prevention and prompt
// injection. Scope is a human-readable string: "" for a full-file read,
// "symbol:Run" for a symbol read, "L10-50" for an offset/limit range.
type ReadRecord struct {
	Path  string `json:"path"`
	Scope string `json:"scope"`
}

type FileCollapse struct {
	Path  string           `json:"path"`
	Level CompressionLayer `json:"level"`
}

type CallChainEntry struct {
	Stage     Stage     `json:"stage"`
	ToolName  string    `json:"tool_name,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type Decision struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type PlanStep struct {
	Step   int    `json:"step"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

type WorkingSet struct {
	Files   []FileRef   `json:"files"`
	Symbols []SymbolRef `json:"symbols"`
}

type FileRef struct {
	Path  string `json:"path"`
	Rev   string `json:"rev,omitempty"`
	Lines string `json:"lines,omitempty"`
	Notes string `json:"notes,omitempty"`
}

type SymbolRef struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// Dimension is a single scoring criterion for eval records.
type Dimension struct {
	Name        string  `json:"name"`
	Score       float64 `json:"score"`
	Weight      float64 `json:"weight"`
	Evidence    string  `json:"evidence"`
	Issue       string  `json:"issue"`
	Improvement string  `json:"improvement"`
}

type AmbiguityResult struct {
	Score     float64  `json:"score"`
	Missing   []string `json:"missing"`
	Questions []string `json:"questions"`
}

type DesignReview struct {
	Verdict string        `json:"verdict"`
	Issues  []DesignIssue `json:"issues"`
}

type DesignIssue struct {
	Pattern     string `json:"pattern"`
	Severity    string `json:"severity"`
	What        string `json:"what"`
	Why         string `json:"why"`
	Alternative string `json:"alternative"`
}

type ScopeResult struct {
	Allowed bool     `json:"allowed"`
	Reasons []string `json:"reasons,omitempty"`
}

// DebateRoundPhase labels the phase of a single debate round.
type DebateRoundPhase string

const (
	DebateProposal  DebateRoundPhase = "proposal"
	DebateChallenge DebateRoundPhase = "challenge"
	DebateRebuttal  DebateRoundPhase = "rebuttal"
	DebateFinal     DebateRoundPhase = "final"
)

// DebateRound captures one round of the debate arena.
type DebateRound struct {
	Phase   DebateRoundPhase `json:"phase"`
	Outputs []DebateOutput   `json:"outputs"`
}

// DebateOutput is one member's contribution in a debate round.
type DebateOutput struct {
	MemberID string   `json:"member_id"`
	Content  string   `json:"content"`
	Targets  []string `json:"targets"` // member IDs this output targets (challenge/rebuttal)
}

// RoundtablePhase describes which stage of the roundtable we are in.
type RoundtablePhase int

const (
	RoundtableIdle           RoundtablePhase = iota
	RoundtableProposal                        // 提案轮
	RoundtableChallenge                       // 质询轮
	RoundtableRebuttal                        // 反驳轮
	RoundtableFinal                           // 终陈轮
	RoundtableAwaitingVerdict                 // 等待用户裁决
	RoundtableDone                            // 完成
)

func (p RoundtablePhase) String() string {
	switch p {
	case RoundtableProposal:
		return "proposal"
	case RoundtableChallenge:
		return "challenge"
	case RoundtableRebuttal:
		return "rebuttal"
	case RoundtableFinal:
		return "final"
	case RoundtableAwaitingVerdict:
		return "awaiting_verdict"
	case RoundtableDone:
		return "done"
	default:
		return "idle"
	}
}

// RoundtableState tracks the current roundtable session within TaskState.
type RoundtableState struct {
	Goal         string             `json:"goal"`
	Phase        RoundtablePhase    `json:"phase"`
	Members      []RoundtableMember `json:"members"`
	DebateRounds []DebateRound      `json:"debate_rounds"` // 替代 Proposals + Reviews
}
