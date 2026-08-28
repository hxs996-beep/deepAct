package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentsFile 是一份加载到的 AGENTS.md 内容。
type AgentsFile struct {
	Source  string // "~/.deepact/AGENTS.md" 或 "<projectRoot>/AGENTS.md"
	Content string // 文件内容（trim 后）
}

// LoadAgentsMD 级联加载 AGENTS.md：用户级（~/.deepact/AGENTS.md）在前（低优先），
// 项目级（<projectRoot>/AGENTS.md）在后（高优先，更接近任务）。
// 缺失或空白文件静默跳过。顺序即注入顺序。
func LoadAgentsMD(projectRoot string) []AgentsFile {
	var files []AgentsFile
	if home, err := os.UserHomeDir(); err == nil {
		if content, ok := readAgentsMD(filepath.Join(home, ".deepact", "AGENTS.md")); ok {
			files = append(files, AgentsFile{Source: "~/.deepact/AGENTS.md", Content: content})
		}
	}
	if projectRoot != "" {
		if content, ok := readAgentsMD(filepath.Join(projectRoot, "AGENTS.md")); ok {
			files = append(files, AgentsFile{Source: "AGENTS.md", Content: content})
		}
	}
	return files
}

// readAgentsMD 读取文件并 trim，空白内容视为缺失。
func readAgentsMD(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false // 缺失或读取失败均静默跳过（AGENTS.md 缺失是常态）
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", false
	}
	return content, true
}

// RenderAgentsBlock 把加载到的 AGENTS.md 渲染为稳定区段落。
// 无文件时返回空字符串（输出与现状完全一致，零回归）。
func RenderAgentsBlock(files []AgentsFile) string {
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Project Conventions (AGENTS.md)\n")
	b.WriteString("以下内容来自 AGENTS.md 约定文件，作为固定上下文生效。\n")
	for _, f := range files {
		fmt.Fprintf(&b, "\n### %s\n\n%s\n", f.Source, f.Content)
	}
	return b.String()
}
