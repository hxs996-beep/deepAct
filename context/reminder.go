package context

import (
	"fmt"
	"strings"

	"github.com/deepact/deepact/engine"
)

// zhFromLang maps a userLang value ("中文" when Chinese) to a bool.
func zhFromLang(userLang string) bool { return userLang == "中文" }

func BuildTaskReminder(state *engine.TaskState, userLang string) string {
	if state == nil {
		return ""
	}
	zh := zhFromLang(userLang)
	var b strings.Builder
	b.WriteString("[TASK REMINDER]\n")

	if state.Goal != "" {
		b.WriteString(fmt.Sprintf("%s %s\n", pickLabel(zh, "Goal:", "目标："), state.Goal))
	}
	if len(state.MemoryMarkers) > 0 {
		b.WriteString(pickLabel(zh, "Key findings (preserved across context):\n", "关键发现（跨上下文保留）：\n"))
		for _, m := range state.MemoryMarkers {
			b.WriteString(fmt.Sprintf("  ⚡ %s\n", m))
		}
	}
	if len(state.Decisions) > 0 {
		b.WriteString(pickLabel(zh, "Decisions:\n", "决策：\n"))
		for _, d := range state.Decisions {
			b.WriteString(fmt.Sprintf("  - %s\n", d.Text))
		}
	}
	if len(state.ModifiedFiles) > 0 {
		b.WriteString(fmt.Sprintf("%s %s\n", pickLabel(zh, "Modified:", "已修改："), strings.Join(state.ModifiedFiles, ", ")))
	}
	if len(state.OpenQuestions) > 0 {
		b.WriteString(pickLabel(zh, "Open questions:\n", "待解决问题：\n"))
		for _, q := range state.OpenQuestions {
			b.WriteString(fmt.Sprintf("  - %s\n", q))
		}
	}
	if state.ActiveSkillName != "" {
		b.WriteString(fmt.Sprintf(pickLabel(zh, "Active skill: %s — Follow its methodology precisely.\n", "当前技能：%s — 严格遵循其方法流程。\n"), state.ActiveSkillName))
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
			b.WriteString(fmt.Sprintf("%s %s\n", pickLabel(zh, "Current step:", "当前步骤："), current))
		}
	}

	result := b.String()
	if result == "[TASK REMINDER]\n" {
		return ""
	}
	return result
}

// pickLabel returns the Chinese label when zh is true, otherwise the English label.
func pickLabel(zh bool, en, zhLabel string) string {
	if zh {
		return zhLabel
	}
	return en
}
