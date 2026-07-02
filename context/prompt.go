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
// Headers are rendered in the user's language to reinforce consistent output language
// across turns — English structural text in a Chinese session pulls the model toward
// mixed-language responses.
func BuildBlockB(taskState string, userLang string) string {
	isZH := userLang == "中文"
	var builder strings.Builder
	if isZH {
		builder.WriteString("# Block B：运行时上下文\n\n")
		builder.WriteString("## 任务状态（原文）\n")
	} else {
		builder.WriteString("# Block B: Runtime Context\n\n")
		builder.WriteString("## Task State (verbatim)\n")
	}
	if strings.TrimSpace(taskState) == "" {
		if isZH {
			builder.WriteString("（空）\n")
		} else {
			builder.WriteString("(empty)\n")
		}
	} else {
		builder.WriteString(taskState)
		builder.WriteString("\n")
	}
	return builder.String()
}

// BuildStableSessionContext returns a user message containing session-stable content
// (environment). This message is at the top of the messages array (after
// system prompt) and stays identical across turns, enabling prefix cache hits.
// Headers are rendered in the user's language — English structural text in a
// Chinese session pulls the model toward mixed-language responses.
func BuildStableSessionContext(envInfo EnvironmentInfo, userLang string) string {
	isZH := userLang == "中文"
	var builder strings.Builder
	if isZH {
		builder.WriteString("# Block S：会话上下文（固定）\n\n")
		builder.WriteString("## 环境\n")
		builder.WriteString(fmt.Sprintf("- 操作系统: %s\n", envInfo.OS))
		builder.WriteString(fmt.Sprintf("- 架构: %s\n", envInfo.Arch))
		builder.WriteString(fmt.Sprintf("- 工作目录: %s\n", envInfo.CWD))
		if envInfo.Date != "" {
			builder.WriteString(fmt.Sprintf("- 日期: %s\n", envInfo.Date))
		}
	} else {
		builder.WriteString("# Block S: Session Context (Stable)\n\n")
		builder.WriteString("## Environment\n")
		builder.WriteString(fmt.Sprintf("- OS: %s\n", envInfo.OS))
		builder.WriteString(fmt.Sprintf("- Arch: %s\n", envInfo.Arch))
		builder.WriteString(fmt.Sprintf("- CWD: %s\n", envInfo.CWD))
		if envInfo.Date != "" {
			builder.WriteString(fmt.Sprintf("- Date: %s\n", envInfo.Date))
		}
	}
	builder.WriteString("\n")
	return builder.String()
}
