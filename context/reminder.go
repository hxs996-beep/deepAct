package context

import (
	"fmt"
	"strings"

	"github.com/deepact/deepact/engine"
)

func BuildTaskReminder(state *engine.TaskState) string {
	if state == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("[TASK REMINDER]\n")

	if state.Goal != "" {
		b.WriteString(fmt.Sprintf("Goal: %s\n", state.Goal))
	}
	if len(state.MemoryMarkers) > 0 {
		b.WriteString("Key findings (preserved across context):\n")
		for _, m := range state.MemoryMarkers {
			b.WriteString(fmt.Sprintf("  ⚡ %s\n", m))
		}
	}
	if len(state.Decisions) > 0 {
		b.WriteString("Decisions:\n")
		for _, d := range state.Decisions {
			b.WriteString(fmt.Sprintf("  - %s\n", d.Text))
		}
	}
	if len(state.ModifiedFiles) > 0 {
		b.WriteString(fmt.Sprintf("Modified: %s\n", strings.Join(state.ModifiedFiles, ", ")))
	}
	if len(state.OpenQuestions) > 0 {
		b.WriteString("Open questions:\n")
		for _, q := range state.OpenQuestions {
			b.WriteString(fmt.Sprintf("  - %s\n", q))
		}
	}
	if len(state.Plan) > 0 {
		current := ""
		for _, step := range state.Plan {
			if step.Status == "in_progress" {
				current = step.Text
				break
			}
		}
		if current != "" {
			b.WriteString(fmt.Sprintf("Current step: %s\n", current))
		}
	}

	result := b.String()
	if result == "[TASK REMINDER]\n" {
		return ""
	}
	return result
}
