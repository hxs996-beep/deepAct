package tools

import "github.com/deepact/deepact/engine"

type EngineExecutor struct {
	exec        *Executor
	registry    *Registry
	ArtifactDir string // base directory for artifact store
	resolvePath func(guess string) (resolved string, alternatives []string) // RepoMap-based path resolver
}

func NewEngineExecutor(registry *Registry) *EngineExecutor {
	return &EngineExecutor{exec: NewExecutor(registry), registry: registry}
}

// SetResolvePath sets the RepoMap-based path resolver for the ReadTool.
// The resolver takes a model-provided path guess and returns the resolved
// path or alternative suggestions from the project's RepoMap.
func (e *EngineExecutor) SetResolvePath(fn func(guess string) (resolved string, alternatives []string)) {
	e.resolvePath = fn
}

func (e *EngineExecutor) Specs() []engine.ModelTool {
	if e == nil || e.registry == nil {
		return nil
	}
	specs := e.registry.AllSpecs()
	result := make([]engine.ModelTool, 0, len(specs))
	for _, spec := range specs {
		result = append(result, engine.ModelTool{
			Type: "function",
			Function: engine.ModelToolFunction{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  spec.Parameters,
			},
		})
	}
	return result
}

func (e *EngineExecutor) Execute(ctx engine.ToolExecContext, calls []engine.ToolCallRequest) []engine.ToolResult {
	if e == nil || e.exec == nil {
		return nil
	}
	toolCtx := ToolContext{WorkDir: ctx.WorkDir, SessionID: ctx.SessionID, TurnNumber: ctx.TurnNumber, ArtifactDir: e.ArtifactDir, ResolvePath: e.resolvePath}
	toolCalls := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		toolCalls = append(toolCalls, ToolCall{ID: call.ID, Name: call.Name, Input: call.Input})
	}
	results := e.exec.Execute(toolCtx, toolCalls)
	engineResults := make([]engine.ToolResult, 0, len(results))
	for _, result := range results {
		engineResults = append(engineResults, engine.ToolResult{
			ToolCallID:  result.ToolCallID,
			ToolName:    result.ToolName,
			Status:      result.Status,
			Digest:      result.Digest,
			ArtifactRef: result.ArtifactRef,
			ExitCode:    result.ExitCode,
		})
	}
	return engineResults
}
