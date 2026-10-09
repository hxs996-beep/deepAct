package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// agentsMDFilename 是每个目录的默认约定文件名。
	agentsMDFilename = "AGENTS.md"
	// agentsMDOverrideFilename 存在且非空时取代同目录的 agentsMDFilename。
	agentsMDOverrideFilename = "AGENTS.override.md"
	// agentsMDProjectMarker 用于向祖先目录定位项目根。
	agentsMDProjectMarker = ".git"
	// agentsMDMaxBytes 是项目级 AGENTS.md 的内容总字节预算。超出时从离 cwd
	// 最远的文件开始整份丢弃；用户级文件不计入，也永不被丢弃。
	agentsMDMaxBytes = 32 * 1024
)

// AgentsFile 是一份加载到的 AGENTS.md 内容。
type AgentsFile struct {
	Source  string // 用户级为 "~/.deepact/AGENTS.md"；项目级为相对项目根的路径（如 "a/b/AGENTS.md"）
	Content string // 文件内容（trim 后）
}

// LoadAgentsMD 级联加载 AGENTS.md，返回顺序即注入顺序（低优先在前）：
//   - 用户级：~/.deepact/AGENTS.md
//   - 项目级：从项目根（含 agentsMDProjectMarker 的最近祖先）逐级向下到 cwd
//
// 同目录内 AGENTS.override.md 取代 AGENTS.md；缺失或空白文件静默跳过。
// 找不到项目标记时只加载 cwd 一个目录，不采纳任意祖先的约定。
// 项目级内容超预算时从离 cwd 最远的文件开始整份丢弃，cwd 那一份始终保留。
func LoadAgentsMD(cwd string) []AgentsFile {
	var files []AgentsFile
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, ".deepact")
		if content, name, ok := readAgentsMDFromDir(dir); ok {
			files = append(files, AgentsFile{Source: "~/.deepact/" + name, Content: content})
		}
	}
	return append(files, loadProjectAgentsMD(cwd)...)
}

// ProjectAgentsMDPath 返回 cwd 对应项目根下的 AGENTS.md 路径。项目根的定义与
// LoadAgentsMD 一致：cwd 自身或最近的含 agentsMDProjectMarker 的祖先；找不到时
// 退化为 cwd。cwd 为空时返回空串。写入方（remember_preference 工具）据此把偏好
// 落到与读取端同一个文件，避免写出一个不会被加载的副本。
func ProjectAgentsMDPath(cwd string) string {
	if cwd == "" {
		return ""
	}
	dirs := projectAgentsMDDirs(filepath.Clean(cwd))
	return filepath.Join(dirs[0], agentsMDFilename)
}

// loadProjectAgentsMD 收集项目级 AGENTS.md，顺序为 项目根 → cwd。
func loadProjectAgentsMD(cwd string) []AgentsFile {
	if cwd == "" {
		return nil
	}
	dirs := projectAgentsMDDirs(filepath.Clean(cwd))
	root := dirs[0] // dirs[0] 即项目根（退化时等于 cwd）
	files := make([]AgentsFile, 0, len(dirs))
	for _, dir := range dirs {
		content, name, ok := readAgentsMDFromDir(dir)
		if !ok {
			continue
		}
		files = append(files, AgentsFile{Source: agentsMDLabel(root, dir, name), Content: content})
	}
	return trimProjectAgentsMDToBudget(files)
}

// projectAgentsMDDirs 返回从项目根到 cwd 的目录链（含两端）。
// 项目根是 cwd 自身或最近的含 agentsMDProjectMarker 的祖先；找不到时退化为 [cwd]。
func projectAgentsMDDirs(cwd string) []string {
	fromCwd := []string{} // cwd → 祖先
	found := false
	for dir := cwd; ; {
		fromCwd = append(fromCwd, dir)
		if hasAgentsMDProjectMarker(dir) {
			found = true
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir { // 已到文件系统根
			break
		}
		dir = parent
	}
	if !found {
		return []string{cwd}
	}
	// fromCwd 是 cwd → ... → 项目根，反转为注入顺序。
	for i, j := 0, len(fromCwd)-1; i < j; i, j = i+1, j-1 {
		fromCwd[i], fromCwd[j] = fromCwd[j], fromCwd[i]
	}
	return fromCwd
}

func hasAgentsMDProjectMarker(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, agentsMDProjectMarker))
	return err == nil
}

// agentsMDLabel 返回相对项目根的展示路径，跨平台统一使用 "/" 分隔，
// 以保证注入字节稳定。项目根自身（或未找到项目根时）退化为文件名。
func agentsMDLabel(root, dir, name string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return name
	}
	return filepath.ToSlash(filepath.Join(rel, name))
}

// readAgentsMDFromDir 读取目录内的约定文件：AGENTS.override.md 优先，
// 缺失或空白时回退 AGENTS.md。
func readAgentsMDFromDir(dir string) (content, name string, ok bool) {
	if content, ok := readAgentsMD(filepath.Join(dir, agentsMDOverrideFilename)); ok {
		return content, agentsMDOverrideFilename, true
	}
	if content, ok := readAgentsMD(filepath.Join(dir, agentsMDFilename)); ok {
		return content, agentsMDFilename, true
	}
	return "", "", false
}

// trimProjectAgentsMDToBudget 从离 cwd 最远的文件开始整份丢弃，直到内容总字节
// 不超过 agentsMDMaxBytes。最接近 cwd 的那一份始终保留（优先级高于预算）。
func trimProjectAgentsMDToBudget(files []AgentsFile) []AgentsFile {
	total := 0
	for _, f := range files {
		total += len(f.Content)
	}
	drop := 0
	for drop < len(files)-1 && total > agentsMDMaxBytes {
		total -= len(files[drop].Content)
		drop++
	}
	return files[drop:]
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
