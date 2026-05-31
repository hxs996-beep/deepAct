package engine

func (e *Engine) getLastUserContent() string {
	for i := len(e.history) - 1; i >= 0; i-- {
		if e.history[i].Role == "user" && e.history[i].Content != "" {
			return e.history[i].Content
		}
	}
	return ""
}

func (e *Engine) getLastAssistantContent() string {
	for i := len(e.history) - 1; i >= 0; i-- {
		if e.history[i].Role == "assistant" && e.history[i].Content != "" {
			return e.history[i].Content
		}
	}
	return ""
}
