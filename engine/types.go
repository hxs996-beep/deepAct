package engine

import (
	"context"
	"encoding/json"
	"errors"
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
	// ID is an optional stable key the model assigns to the step. It exists so
	// a delegated sub-agent can name the step it executes
	// (handoff_to_agent.step) and be nested under it, instead of the UI
	// guessing from step wording. Empty = the step dispatches no work.
	ID      string `json:"id,omitempty"`
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
	// RunID is the stable handle of a depth-0 delegation: the handoff tool
	// call id for a synchronous run, "bg-<seq>" for an async one. agent_start
	// and its agent_done carry the SAME value, so the UI pairs them by
	// identity — concurrent same-role agents are otherwise indistinguishable,
	// and name-based pairing closes the wrong row.
	RunID string
	// Step is the TodoItem.ID this sub-agent executes; empty = not linked to
	// any step, and the UI groups it separately.
	Step string
	// Status is the outcome carried by agent_done: "ok", "error" or
	// "cancelled". Empty on every other event.
	Status string
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
	SessionID             string
	ModelName             string // default (Pro) model name
	FlashModelName        string // Flash model name for cheaper agents
	BaseURL               string // API base URL (e.g. https://api.deepseek.com or https://openrouter.ai/api/v1)
	MaxConcurrentRequests int    // cap on concurrent in-flight LLM requests; 0 = default 8
	// ReasoningEffort sets the DeepSeek thinking effort for all model calls
	// (main loop + sub-agents). Empty = engine default (high). Values: none |
	// low | high | max.
	ReasoningEffort string
	// AgentSpecs are user-defined sub-agent roles loaded from config.toml
	// [agents] section. Registered alongside built-ins; user roles override
	// built-ins with the same name.
	AgentSpecs           []AgentSpec
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
	// SubAgentTokenBudget caps a sub-agent run's billable tokens (cache-miss
	// + completion; cache hits are free). Three-state: 0 = default (2× the
	// sub-agent context window), -1 = unlimited, >0 = explicit cap. Wired via
	// [context].sub_agent_token_budget.
	SubAgentTokenBudget int
	// MaxOutstandingAsyncSubAgents caps dispatched-but-uncollected async
	// sub-agent jobs (a slot frees when agent_poll consumes a done result).
	// 0 = default 8. Wired via [context].max_outstanding_async_subagents.
	MaxOutstandingAsyncSubAgents int
	// MaxSuspendedSubAgents caps how many suspended (awaiting_user) sub-agent
	// jobs are kept at once. Each entry retains a whole history, so the table
	// must stay small. 0 = default 4. Wired via
	// [context].max_suspended_subagents.
	MaxSuspendedSubAgents int
	// MaxConcurrentSubAgents caps how many sub-agent runs may be in flight at
	// once across the WHOLE delegation tree (sync depth 0, async, nested and
	// resumed runs share one counter). 0 = default 6. Wired via
	// [context].max_concurrent_subagents.
	MaxConcurrentSubAgents int
	// MaxSubAgentDepth caps sub-agent nesting. 0 = default 2. Wired via
	// [context].max_subagent_depth.
	MaxSubAgentDepth int
	// RunSubAgentTokenBudget caps the billable tokens (cache-miss + completion;
	// cache hits are free) of every sub-agent run in ONE Run, nested children
	// included. Three-state: 0 = default (20× the sub-agent context window),
	// -1 = unlimited, >0 = explicit cap. Wired via
	// [context].run_sub_agent_token_budget.
	RunSubAgentTokenBudget int
	ShowThinking           bool    // stream model reasoning/thinking to UI
	RiskThreshold          float64 // router risk threshold for Pro/Flash escalation
	// Approval policy for bash commands (see engine/approval.go). ApprovalMode
	// is on-request (default) or never; ApprovalRules are user-declared rules
	// from [approval.rules]; LearnedApprovals are commands the user confirmed in
	// earlier sessions; OnApprovalLearned persists a new confirmation (nil keeps
	// it session-only). Wired in cmd/run.go from config plus the approvals store.
	ApprovalMode      string
	ApprovalRules     []ApprovalRule
	LearnedApprovals  []string
	OnApprovalLearned func(command string)
	WorkDir           string
	OnProgress        ProgressFunc
	Pricing           PricingConfig
	EvalStoreDir      string // directory for evaluation records JSONL (default: ~/.deepact/eval/)
	PromptVersion     string // SHA256 hash of the system prompt for tracking
}

type EngineResponse struct {
	Summary      string   `json:"summary"`
	Questions    []string `json:"questions,omitempty"`
	Options      []string `json:"options,omitempty"` // e.g. ["用Redis", "用MySQL"]
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
	// RunID is the job handle of the sub-agent that asked (empty when the main
	// agent asked itself). The answer goes back through agent_resume(RunID, …).
	RunID string `json:"run_id,omitempty"`
}

type ModelRequest struct {
	Model           string
	Messages        []ModelMessage
	Tools           []ModelTool
	Temperature     float64
	MaxTokens       int
	ReasoningEffort string
	JsonMode        bool
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

	// Declarative tool semantics, mirrored from tools.ToolSpec by the adapter.
	// The engine schedules calls and scores progress from THESE, never from a
	// tool-name list.
	Mutates   bool
	NovelInfo bool
	// Sequential: run this call alone with progressive display; false = batch it
	// with its siblings.
	Sequential bool
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

// Model error sentinels surfaced by the LLM adapter. The engine checks these
// (via errors.Is) to decide how to handle a failed request: rate limits are
// transient and worth a silent in-loop retry, while insufficient balance is
// persistent and needs a clear user-facing message.
var (
	ErrModelRateLimit       = errors.New("model rate limit")
	ErrModelInsufficientBal = errors.New("model insufficient balance")
	// ErrModelContextLength surfaces a provider context-window overflow (HTTP
	// 400). The engine reacts by shrinking the history and retrying once.
	ErrModelContextLength = errors.New("model context length exceeded")
)

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
	// RunID is the job handle of a sub-agent that suspended on a question, so
	// the parent run can stitch the resume cascade without parsing digest text.
	// Empty on every other path.
	RunID string `json:"run_id,omitempty"`
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
