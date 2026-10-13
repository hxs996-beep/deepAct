package context

import (
	"fmt"
	"strings"
)

type EnvironmentInfo struct {
	OS      string
	Arch    string
	CWD     string
	Date    string
	DirTree string // compact codebase directory snapshot (built once at startup)
}

// BuildStableSessionContext returns a user message containing session-stable content
// (environment). This message is at the top of the messages array (after
// system prompt) and stays identical across turns, enabling prefix cache hits.
// The prompt is English-only, so the headers are English regardless of the
// user's language; the system prompt tells the model to answer in the user's.
func BuildStableSessionContext(envInfo EnvironmentInfo) string {
	var builder strings.Builder
	builder.WriteString("# Block S: Session Context (Stable)\n\n")
	builder.WriteString("## Environment\n")
	builder.WriteString(fmt.Sprintf("- OS: %s\n", envInfo.OS))
	builder.WriteString(fmt.Sprintf("- Arch: %s\n", envInfo.Arch))
	builder.WriteString(fmt.Sprintf("- CWD: %s\n", envInfo.CWD))
	if envInfo.Date != "" {
		builder.WriteString(fmt.Sprintf("- Date: %s\n", envInfo.Date))
	}
	if envInfo.DirTree != "" {
		builder.WriteString("\n## Codebase\n")
		builder.WriteString("A snapshot of the project directory tree (generated at startup, language-agnostic, truncated). " +
			"Use it to orient quickly; for precise detail use grep or LSP.\n")
		builder.WriteString("\n```\n")
		builder.WriteString(envInfo.DirTree)
		builder.WriteString("\n```\n")
	}
	builder.WriteString("\n")
	return builder.String()
}
