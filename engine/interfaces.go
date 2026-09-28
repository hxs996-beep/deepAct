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

type ContextBuilder interface {
	Build(state *TaskState, history []Message, toolResults []ToolResult) []ModelMessage
	EstimateTokens(messages []ModelMessage) int
	// InjectedBlocks returns the prompt blocks this builder injects into the
	// model input (system prompt, stable session context, skills list). The
	// engine derives its echoed-block stripping headers from them, so the
	// stripper always reflects what was actually sent.
	InjectedBlocks() []string
}

type Compressor interface {
	ShouldCompress(currentTokens int, maxTokens int) (CompressionLayer, bool)
	Compress(ctx context.Context, layer CompressionLayer, state *TaskState, history []Message) ([]Message, error)
	SetUserLang(lang UserLanguage)
}

type SessionStore interface {
	AppendEvent(event Event) error
	LoadEvents(sessionID string) ([]Event, error)
}

// MemoryStore persists cross-session memory snapshots per project. Implemented
// by the memory package; injected via EngineDeps so engine never imports
// memory (which imports engine for the MemorySnapshot type).
type MemoryStore interface {
	Load() (*MemorySnapshot, error)
	Save(snap *MemorySnapshot) error
	Clear() error
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
