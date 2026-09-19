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
	if envInfo.DirTree != "" {
		if isZH {
			builder.WriteString("\n## 代码库结构\n")
			builder.WriteString("以下是项目目录树快照（启动时生成，语言无关，已裁剪）。用于快速定位文件归属；精确信息用 grep 或 LSP。\n")
		} else {
			builder.WriteString("\n## Codebase\n")
			builder.WriteString("A snapshot of the project directory tree (generated at startup, language-agnostic, truncated). " +
				"Use it to orient quickly; for precise detail use grep or LSP.\n")
		}
		builder.WriteString("\n```\n")
		builder.WriteString(envInfo.DirTree)
		builder.WriteString("\n```\n")
	}
	builder.WriteString("\n")
	return builder.String()
}
