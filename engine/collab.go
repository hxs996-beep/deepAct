package engine

import "strings"

// CollabCommand represents a parsed /collab command.
type CollabCommand struct {
	Goal string
}

// parseCollabCommand checks if userMsg is a /collab command.
func parseCollabCommand(userMsg string) *CollabCommand {
	trimmed := strings.TrimSpace(userMsg)
	if trimmed == "" {
		return nil
	}
	lines := strings.SplitN(trimmed, "\n", 2)
	firstLine := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(firstLine, "/") {
		return nil
	}
	rest := firstLine[1:]
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.ToLower(strings.TrimSpace(parts[0]))
	if cmd != "collab" {
		return nil
	}
	goal := strings.Join(parts[1:], " ")
	if goal == "" {
		return nil
	}
	return &CollabCommand{Goal: goal}
}

// CollabHall orchestrates the /collab pipeline.
type CollabHall struct {
	engine *Engine
}

func NewCollabHall(e *Engine) *CollabHall {
	return &CollabHall{engine: e}
}
