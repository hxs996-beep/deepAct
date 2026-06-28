package context

import (
	"fmt"
	"strings"
)



type EnvironmentInfo struct {
	OS   string
	Arch string
	CWD  string
	Date string
}

// BuildBlockB renders the volatile tail (Block B) — a small (~200 tokens) JSON block
// of runtime TaskState fields that change every turn. Placed after full history so that
// the history prefix remains cacheable; only this tail and new messages cause cache miss.
// See docs/cache-refactor-plan.md for the full architecture rationale.
// No language directive is needed — the system prompt is already in the correct language.
func BuildBlockB(taskState string, userLang string) string {
	var builder strings.Builder
	builder.WriteString("# Block B: Runtime Context\n\n")
	builder.WriteString("## Task State (verbatim)\n")
	if strings.TrimSpace(taskState) == "" {
		builder.WriteString("(empty)\n")
	} else {
		builder.WriteString(taskState)
		builder.WriteString("\n")
	}
	return builder.String()
}

// BuildStableSessionContext returns a user message containing session-stable content
// (deepact.md, environment). This message is at the top of the messages array (after
// system prompt) and stays identical across turns, enabling prefix cache hits.
// No language directive is needed — the system prompt is already in the correct language.
func BuildStableSessionContext(deepactMD string, envInfo EnvironmentInfo, userLang string) string {
	var builder strings.Builder
	builder.WriteString("# Block S: Session Context (Stable)\n\n")
	if strings.TrimSpace(deepactMD) != "" {
		builder.WriteString("## deepact.md\n")
		builder.WriteString(deepactMD)
		if !strings.HasSuffix(deepactMD, "\n") {
			builder.WriteString("\n")
		}
		builder.WriteString("\n")
	}
	// Environment — session-stable, moved from Block B to prefix zone for cache hits.
	builder.WriteString("## Environment\n")
	builder.WriteString(fmt.Sprintf("- OS: %s\n", envInfo.OS))
	builder.WriteString(fmt.Sprintf("- Arch: %s\n", envInfo.Arch))
	builder.WriteString(fmt.Sprintf("- CWD: %s\n", envInfo.CWD))
	if envInfo.Date != "" {
		builder.WriteString(fmt.Sprintf("- Date: %s\n", envInfo.Date))
	}
	builder.WriteString("\n")
	return builder.String()
}
