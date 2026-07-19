package engine

import "context"

type ModelClient interface {
	Stream(ctx context.Context, req ModelRequest) (<-chan ModelChunk, error)
	Complete(ctx context.Context, req ModelRequest) (*ModelResponse, error)
}

type ToolExecutor interface {
	Execute(ctx ToolExecContext, calls []ToolCallRequest) []ToolResult
	Specs() []ModelTool
}

type PolicyChecker interface {
	CheckAmbiguity(userMsg string, state *TaskState) AmbiguityResult
	CheckDesign(plan string, context string) DesignReview
	CheckScope(action string, state *TaskState) ScopeResult
}

type ContextBuilder interface {
	Build(state *TaskState, history []Message, toolResults []ToolResult) []ModelMessage
	EstimateTokens(messages []ModelMessage) int
	// SetActiveSkill injects the active skill's methodology into the stable zone.
	// Called on skill activation, chain-switch, and deactivation (empty strings).
	// This ensures skill instructions remain visible regardless of conversation length.
	SetActiveSkill(name, content string)
}

type Compressor interface {
	ShouldCompress(currentTokens int, maxTokens int) (CompressionLayer, bool)
	Compress(layer CompressionLayer, state *TaskState, history []Message) ([]Message, error)
	SetUserLang(lang string)
}

type SessionStore interface {
	AppendEvent(event Event) error
	LoadEvents(sessionID string) ([]Event, error)
}

type ModelRouter interface {
	SelectModel(ctx RouteContext) RouteDecision
}

type RouteContext struct {
	AmbiguityScore   float64
	ToolFailureCount int
	EditScopeFiles   int
	ConsecutiveFails int
	IsReadOnly       bool
}

type RouteDecision struct {
	Model     string
	Reasoning string
}
