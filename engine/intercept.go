package engine

import "context"

// interceptedTool is an engine-side tool: its calls are answered by the engine
// itself and must never be dispatched to the tool registry.
//
// This table is the SINGLE source of truth for that. Both the interception
// dispatch and the exclusion of these names from the regular tool batch read it,
// so adding a tool cannot leave the two out of sync — the failure mode where one
// tool_call_id received two responses ("tool not found" from the registry on top
// of the engine's own answer), which the API rejects with a 400 and which then
// poisons every later request in the session.
type interceptedTool struct {
	name string
	// early interceptors run BEFORE the turn's early-return blocks
	// (task_complete, blocked calls), because their side effects must happen even
	// when the turn ends there: a todo snapshot arriving in the same assistant
	// message has to reach the UI, or the step panel freezes on a stale state.
	// Their messages are appended in table order with everyone else's.
	early bool
	// handle returns one tool message per call it owns. Returning no message for
	// a call it does not own is expected: every handler filters by name.
	handle func(e *Engine, ctx context.Context, calls []ToolCallRequest) []Message
}

// interceptedTools lists every engine-side tool, in the order their tool
// messages are appended to history. That order is part of the API contract
// (each message closes a tool_call_id of the preceding assistant message), so
// new entries go at the end unless there is a reason.
func interceptedTools() []interceptedTool {
	return []interceptedTool{
		{name: LoadSkillToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processLoadSkillCalls(calls)
		}},
		{name: PlanTaskToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processPlanTaskCalls(calls)
		}},
		{name: TodoWriteToolName, early: true, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processTodoWriteCalls(calls)
		}},
		{name: AskUserToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processAskUserCalls(calls)
		}},
		{name: AgentResumeToolName, handle: func(e *Engine, ctx context.Context, calls []ToolCallRequest) []Message {
			return e.processAgentResumeCalls(ctx, calls)
		}},
		{name: AgentPollToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processAgentPollCalls(calls)
		}},
		{name: ListAgentsToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processListAgentsCalls(calls)
		}},
		{name: WaitAgentToolName, handle: func(e *Engine, ctx context.Context, calls []ToolCallRequest) []Message {
			return e.processWaitAgentCalls(ctx, calls)
		}},
		{name: SendMessageToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processSendMessageCalls(calls)
		}},
		{name: InterruptAgentToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processInterruptAgentCalls(calls)
		}},
		{name: CloseAgentToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processCloseAgentCalls(calls)
		}},
	}
}

// interceptedToolNames derives the exclusion set from the table above, so the
// "answered by the engine" and "not dispatched to the registry" views can never
// disagree.
func interceptedToolNames() map[string]bool {
	table := interceptedTools()
	set := make(map[string]bool, len(table))
	for _, it := range table {
		set[it.name] = true
	}
	return set
}
