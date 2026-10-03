package engine

import "strconv"

// Forking gives a sub-agent the parent's conversation prefix as its starting
// context, instead of forcing the delegating model to re-narrate what it
// already knows. The copy is deliberately conservative: it always ends on a
// USER message boundary.
//
// Why that boundary: a user message can never leave a dangling tool_call_id
// (every tool call before it must already have been answered, or the parent
// loop would have failed), and it excludes the in-flight assistant message that
// carries THIS handoff call — which has no tool response yet and would violate
// the assistant(tool_calls) → tool contract inside the sub-agent's history.

// forkHistoryFor materializes the fork spec into messages. Spec: "" or "none"
// = no fork; "all" = the whole safe prefix; a positive integer N = the last N
// user turns. Anything else is ignored (no fork) rather than guessed at — a
// malformed spec must not silently widen what a sub-agent can see.
func (e *Engine) forkHistoryFor(spec string) []ModelMessage {
	switch spec {
	case "", "none":
		return nil
	case "all":
		return toModelMessages(forkablePrefix(e.history))
	}
	n, err := strconv.Atoi(spec)
	if err != nil || n <= 0 {
		return nil
	}
	return toModelMessages(forkablePrefix(lastNUserTurns(e.history, n)))
}

// forkablePrefix returns history up to and including the LAST user message.
// Returns nil when there is no user message (nothing safe to copy).
func forkablePrefix(history []Message) []Message {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return history[:i+1]
		}
	}
	return nil
}

// lastNUserTurns returns the suffix of history starting at the Nth-from-last
// user message. Asking for more turns than exist returns history unchanged.
func lastNUserTurns(history []Message, n int) []Message {
	seen := 0
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != "user" {
			continue
		}
		seen++
		if seen == n {
			return history[i:]
		}
	}
	return history
}

// toModelMessages converts conversation Messages into the sub-agent's model
// history shape, preserving tool calls and tool-call ids.
func toModelMessages(history []Message) []ModelMessage {
	if len(history) == 0 {
		return nil
	}
	out := make([]ModelMessage, 0, len(history))
	for _, m := range history {
		mm := ModelMessage{
			Role:             m.Role,
			Content:          m.Content,
			ToolCallID:       m.ToolCallID,
			ReasoningContent: m.ReasoningContent,
		}
		for _, tc := range m.ToolCalls {
			mm.ToolCalls = append(mm.ToolCalls, ModelToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: ModelFunctionCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
		out = append(out, mm)
	}
	return out
}
