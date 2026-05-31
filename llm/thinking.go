package llm

import "sync"

// ReasoningEchoManager preserves reasoning_content when tool calls are present.
// DeepSeek expects the opaque reasoning_content to be echoed verbatim in the next turn
// when the assistant produced tool_calls in the previous turn.
type ReasoningEchoManager struct {
	mu            sync.Mutex
	lastReasoning string
	needsEcho     bool
}

func NewReasoningEchoManager() *ReasoningEchoManager {
	return &ReasoningEchoManager{}
}

func (m *ReasoningEchoManager) ObserveAssistant(msg Message) {
	if msg.ReasoningContent == "" {
		return
	}
	if len(msg.ToolCalls) == 0 {
		m.mu.Lock()
		m.lastReasoning = ""
		m.needsEcho = false
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	m.lastReasoning = msg.ReasoningContent
	m.needsEcho = true
	m.mu.Unlock()
}

func (m *ReasoningEchoManager) PendingEcho() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.needsEcho || m.lastReasoning == "" {
		return "", false
	}
	return m.lastReasoning, true
}

func (m *ReasoningEchoManager) Clear() {
	m.mu.Lock()
	m.lastReasoning = ""
	m.needsEcho = false
	m.mu.Unlock()
}

func (m *ReasoningEchoManager) ApplyEcho(msgs []Message) []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.needsEcho || m.lastReasoning == "" {
		return msgs
	}
	if len(msgs) == 0 {
		debugLog.Println("warning: ReasoningEchoManager needs echo but message list is empty")
		return msgs
	}
	idx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			idx = i
			break
		}
	}
	if idx == -1 {
		// No assistant message to fill — clear needsEcho to prevent repeated 400
		debugLog.Println("warning: ReasoningEchoManager needs echo but no assistant message found — clearing")
		m.needsEcho = false
		return msgs
	}
	if msgs[idx].ReasoningContent != "" {
		return msgs
	}
	msgs[idx].ReasoningContent = m.lastReasoning
	m.needsEcho = false
	return msgs
}

// applyEchoIfNeeded ensures all assistant messages with tool_calls have reasoning_content.
// DeepSeek requires reasoning_content to be echoed when tool_calls are present.
// After compression (which may strip reasoning), this function restores it.
func applyEchoIfNeeded(msgs []Message) []Message {
	if len(msgs) == 0 {
		return msgs
	}

	// Find the most recent assistant with tool_calls that HAS reasoning_content (source)
	var sourceReasoning string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 && msgs[i].ReasoningContent != "" {
			sourceReasoning = msgs[i].ReasoningContent
			break
		}
	}
	if sourceReasoning == "" {
		return msgs // no source to echo from
	}

	// Fill all assistant messages with tool_calls that are MISSING reasoning_content
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 && msgs[i].ReasoningContent == "" {
			msgs[i].ReasoningContent = sourceReasoning
		}
	}
	return msgs
}
