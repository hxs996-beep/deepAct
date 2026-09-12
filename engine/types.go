package engine

import (
	"context"
	"encoding/json"
	"time"
)

type Stage int

const (
	StageIntake Stage = iota
	StagePlan
	StageDesignGuard
	StageAct
	StageVerifyCompact
)

type CompressionLayer int

const (
	LayerToolGovernance CompressionLayer = iota
	LayerFullCompact
)

// TodoItem is a generic step-tracking item reported by the todo_write tool.
// It is skill-agnostic: any skill can instruct the model to report its
// step-by-step progress through this channel, and the UI renders it as a
// plain-text todo list (no skill-specific theming).
type TodoItem struct {
	Content string `json:"content"` // step description (plain text)
	Status  string `json:"status"`  // "pending" | "in_progress" | "completed"
}

type ProgressEvent struct {
	Type       string // "tool_start" | "tool_done" | "thinking" | "content_delta" | "reasoning_delta" | "agent_start" | "agent_done" | "usage" | "todo_update"
	Name       string
	Detail     string // brief digest for live display
	FullDetail string // full content (e.g., diff) for final rendering
	Usage      *ModelUsage
	ModelName  string // which model was used for this API call
	// Todos carries the full todo-list snapshot for Type == "todo_update".
	Todos []TodoItem
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
	SessionID            string
	ModelName            string // default (Pro) model name
	FlashModelName       string // Flash model name for cheaper agents
	BaseURL              string // API base URL (e.g. https://api.deepseek.com or https://openrouter.ai/api/v1)
	SubAgentBaseURL      string // separate API base URL for sub-agents (cache isolation); empty = same as BaseURL
	MaxTurns             int
	MaxIterationsPerTurn int
	MaxContextTokens     int
	// MaxOutputTokens caps the LLM completion length per turn (max_tokens).
	// DeepSeek's 1M context window supports large completions; a generous
	// budget lets the model emit full code edits in one turn. 0 = use the
	// DefaultMaxOutputTokens const.
	MaxOutputTokens        int
	PlanningEnabled        bool
	PlanningThresholdChars int
	AutoConfirmScope       bool
	ShowThinking           bool    // stream model reasoning/thinking to UI
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

// AskUserRequest captures an ask_user tool call: the question the model needs
// the user to answer, plus optional candidate answers. Stored on the Engine
// while awaiting the user's response; consumed by handleConfirmCommand (with
// options) or cleared on free input (without options).
type AskUserRequest struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
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
	// Ctx propagates the run's cancellation signal into tools (sub-agent
	// delegation needs it to cancel child runs when the parent cancels).
	Ctx context.Context
	// Depth is the depth of the NEW sub-agent this handoff produces:
	// 0 = main agent delegating the first level; d+1 = a sub-agent at depth d.
	Depth int
	// UserLang is the session language ("中文" or "") for localized tool output.
	UserLang string
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
	// FinishReason carries a handoff's HandoffResult.FinishReason so the
	// parent loop can react to why a sub-agent ended without text parsing.
	FinishReason string `json:"finish_reason,omitempty"`
	// Questions carries ask_user questions bubbled up from a sub-agent so the
	// parent engine can present them via the awaiting_user path.
	Questions []string `json:"questions,omitempty"`
}

// EventTypeMessage records conversation messages (user/assistant in full,
// tool as a brief digest) for /resume session restoration.
const EventTypeMessage = "message"

type Event struct {
	SessionID string          `json:"session_id"`
	WorkDir   string          `json:"work_dir,omitempty"`
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
	PendingDangerousCmd string           `json:"pending_dangerous_cmd,omitempty"` // normalized command awaiting user confirmation
	Roundtable          *RoundtableState `json:"roundtable,omitempty"`
	Collab              *CollabState     `json:"collab,omitempty"`

	// ReadHistory records each file read this session (path + scope) for the
	// loop guard to count repeated reads of the same (path, scope) and block
	// read loops. It is NOT rendered into the prompt — the read stub + loop
	// guard enforce re-read prevention in-engine, and rendering the full list
	// leaked stale all-session state and grew the volatile tail.
	ReadHistory []ReadRecord `json:"read_history"`

	// PlanConfirmed is set when the user confirms a plan presented by the
	// agent. When true, the plan gate is skipped, allowing edits to proceed.
	// Scoped to a single Run: reset to false at the start of every Run.
	PlanConfirmed bool `json:"plan_confirmed,omitempty"`
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

// MemorySnapshot is the cross-session persisted subset of TaskState. The
// memory store saves it per-project to ~/.deepact/memory/<cwd-hash>/ and the
// engine merges it back into TaskState at startup, so these fields survive
// process restarts and are injected into Block B on every turn.
type MemorySnapshot struct {
	CWD           string     `json:"cwd,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at,omitempty"`
	MemoryMarkers []string   `json:"memory_markers,omitempty"`
	Decisions     []Decision `json:"decisions,omitempty"`
	OpenQuestions []string   `json:"open_questions,omitempty"`
	Assumptions   []string   `json:"assumptions,omitempty"`
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
	RoundtableIdle            RoundtablePhase = iota
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
	// SharedContext 是预搜索子 agent 产出的代码调研报告，作为所有辩论成员的共享基线。
	SharedContext string `json:"shared_context,omitempty"`
	// WinnerID 是终陈后判定的平均分最高成员 ID。
	WinnerID string `json:"winner_id,omitempty"`
	// Blueprint 是胜者方案的详细实施蓝图（LLM 生成）。
	Blueprint string `json:"blueprint,omitempty"`
}

// CollabStageName labels a single stage of the /collab pipeline.
type CollabStageName string

const (
	CollabRecon  CollabStageName = "recon"  // 侦察：扫描代码库
	CollabDesign CollabStageName = "design" // 设计：出技术方案
	CollabDev    CollabStageName = "dev"    // 开发：产实现内容
	CollabReview CollabStageName = "review" // 把关：评审挑问题
)

// CollabStage captures one pipeline stage's output.
type CollabStage struct {
	Name    CollabStageName `json:"name"`
	Content string          `json:"content"`
}

// CollabPhase describes which stage of the /collab pipeline we are in.
type CollabPhase int

const (
	CollabIdle                 CollabPhase = iota
	CollabReconPhase                       // 侦察
	CollabDesignPhase                      // 设计
	CollabDevPhase                         // 开发
	CollabReviewPhase                      // 把关
	CollabAwaitingConfirmation             // 等待用户确认汇总
	CollabDone                             // 完成
)

// CollabState tracks the current /collab pipeline within TaskState.
type CollabState struct {
	Goal   string        `json:"goal"`
	Phase  CollabPhase   `json:"phase"`
	Stages []CollabStage `json:"stages"` // 各流水线段产出，按执行顺序
}
