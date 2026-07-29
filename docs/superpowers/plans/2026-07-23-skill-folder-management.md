# Skill Folder Management Alignment with Claude Code 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 将 DeepAct 的 skill 目录管理改为与 Claude Code 一致的 `<name>/SKILL.md` 目录格式，移除 TOML 支持，并添加全部 14 个 Claude Code frontmatter 字段。

**架构：** Loader 仅扫描子目录中的 `SKILL.md` 文件（Markdown + YAML frontmatter）。SkillFile/Skill 结构体新增 15 个字段。Markdown 解析器增强以支持多行 YAML 列表和布尔值。skill_install 保存为目录格式。buildSkillsBlock 过滤 disable-model-invocation 并展示 when_to_use。

**技术栈：** Go, YAML frontmatter parsing (手写解析器), filepath/os 标准库

---

## 文件结构

| 文件 | 职责 | 操作 |
|------|------|------|
| `skill/skill.go` | Skill/GateConfig 结构体定义 | 修改：新增 15 字段，移除 toml 标签 |
| `skill/loader.go` | SkillFile 结构体 + LoadExternalSkills + skillFromSkillFile | 修改：移除 TOML，目录格式，新增字段 |
| `skill/markdown.go` | ParseMarkdownSkill YAML frontmatter 解析 | 修改：解析 14 个新字段 + 多行列表 + 布尔值 |
| `tools/builtin/skill_install.go` | skill_install 工具 | 修改：MD-only，目录格式保存 |
| `cmd/run.go` | buildSkillsBlock | 修改：过滤 + when_to_use |
| `skill/loader_test.go` | Loader 测试 | 修改：目录格式测试 |
| `skill/markdown_test.go` | Markdown 解析测试 | 修改：新增字段测试 |

**注意：** `BurntSushi/toml` 在 `config/config.go` 和 `engine/roundtable.go` 中也有使用，这两个文件不在本次修改范围内，go.mod 不需要改动。

---

### 任务 1：更新 Skill 结构体（skill/skill.go）

**文件：**
- 修改：`skill/skill.go`

- [ ] **步骤 1：移除 GateConfig 的 toml 标签，新增 Skill 字段**

将 `skill/skill.go` 中的 `GateConfig` 和 `Skill` 结构体替换为：

```go
// GateConfig defines a pre-implementation gate for a skill. When non-nil,
// the engine blocks edit/write calls until the gate is passed (user approval
// or NextSkills transition). Gates are provided by gates.go defaults, not
// by skill files.
type GateConfig struct {
	Type         string   // "path_filter" or "block_all"
	AllowedPaths []string // for "path_filter": paths allowed during gate
}

type Skill struct {
	Name        string   // Unique identifier, e.g. "debugging"
	Description string   // Short description for matching
	Content     string   // Full skill instructions injected into prompt
	Keywords    []string // Retained as metadata (matching is LLM-semantic)
	NextSkills  []string // Skill names suggested after this skill completes
	Gate        *GateConfig // Pre-implementation gate; nil = no gate

	// AutoActivateThreshold is retained as metadata.
	// Unused since keyword-based auto-activation was removed in favor of
	// semantic matching.
	AutoActivateThreshold *int

	// Claude Code-compatible frontmatter fields. Parsed from YAML
	// frontmatter in SKILL.md files.

	AllowedTools           []string // allowed-tools: tool permission patterns
	ArgumentHint           string   // argument-hint: hint showing argument placeholders
	Arguments              []string // arguments: argument names for $name substitution
	WhenToUse              string   // when_to_use: when to auto-invoke, including trigger phrases
	Model                  string   // model: per-skill model override
	Effort                 string   // effort: reasoning effort level
	Agent                  string   // agent: agent type for execution
	Context                string   // context: "fork" for sub-agent, "" for inline
	Hooks                  string   // hooks: JSON-encoded hook configuration
	Paths                  []string // paths: conditional activation file patterns
	UserInvocable          bool     // user-invocable: can be invoked via / (default true)
	DisableModelInvocation bool     // disable-model-invocation: skip auto-activation
	Version                string   // version: skill version string
	Shell                  string   // shell: shell execution settings (JSON-encoded)
	BaseDir                string   // base directory of the skill (for ${SKILL_DIR})
}
```

同时更新 `GateConfig` 上方的注释，将 "skills declare their gate type in TOML" 改为 "gates are provided by gates.go defaults"。

- [ ] **步骤 2：验证编译**

运行：`cd /Users/admin/gitspace/deepact && go build ./skill/`
预期：编译失败，因为 loader.go 仍引用 `sf.Gate` 和 toml 标签（将在任务 2 修复）

---

### 任务 2：更新 SkillFile 和 Loader（skill/loader.go）

**文件：**
- 修改：`skill/loader.go`

- [ ] **步骤 1：重写 loader.go 全文**

将 `skill/loader.go` 全文替换为：

```go
package skill

import (
	"fmt"
	"os"
	"path/filepath"
)

// SkillFile represents the parsed content of a SKILL.md file.
// All fields are populated by ParseMarkdownSkill from YAML frontmatter.
type SkillFile struct {
	Name                  string
	Description           string
	Keywords              []string
	Content               string
	NextSkills            []string
	AutoActivateThreshold *int

	// Claude Code-compatible fields
	AllowedTools           []string
	ArgumentHint           string
	Arguments              []string
	WhenToUse              string
	Model                  string
	Effort                 string
	Agent                  string
	Context                string
	Hooks                  string
	Paths                  []string
	UserInvocable          *bool // nil = default true
	DisableModelInvocation bool
	Version                string
	Shell                  string
	BaseDir                string
}

// LoadExternalSkills loads skill definitions from the given directory.
// Only the directory format is supported: <name>/SKILL.md (Claude Code layout).
//
// Returns nil, nil if the directory doesn't exist.
func LoadExternalSkills(dir string) ([]*Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read skills dir %s: %w", dir, err)
	}

	var skills []*Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue // only <name>/SKILL.md directory format is supported
		}
		// The references/ directory holds reference files namespaced
		// by skill name (references/<skill-name>/), not skill definitions.
		if entry.Name() == "references" {
			continue
		}
		skillMD := filepath.Join(dir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillMD)
		if err != nil {
			continue // subdirectory has no SKILL.md, skip
		}
		sf, err := ParseMarkdownSkill(data)
		if err != nil {
			continue // skip unparseable skill files
		}
		if sf.Name == "" {
			sf.Name = entry.Name() // fallback to directory name
		}
		sf.BaseDir = filepath.Join(dir, entry.Name())
		skills = append(skills, SkillFromSkillFile(sf))
	}
	return skills, nil
}

// SkillFromSkillFile converts a parsed SkillFile into a Skill, applying
// default gate config (from gates.go) and default UserInvocable=true.
func SkillFromSkillFile(sf SkillFile) *Skill {
	s := &Skill{
		Name:                   sf.Name,
		Description:            sf.Description,
		Keywords:               sf.Keywords,
		Content:                sf.Content,
		NextSkills:             sf.NextSkills,
		AutoActivateThreshold:  sf.AutoActivateThreshold,
		AllowedTools:           sf.AllowedTools,
		ArgumentHint:           sf.ArgumentHint,
		Arguments:              sf.Arguments,
		WhenToUse:              sf.WhenToUse,
		Model:                  sf.Model,
		Effort:                 sf.Effort,
		Agent:                  sf.Agent,
		Context:                sf.Context,
		Hooks:                  sf.Hooks,
		Paths:                  sf.Paths,
		DisableModelInvocation: sf.DisableModelInvocation,
		Version:                sf.Version,
		Shell:                  sf.Shell,
		BaseDir:                sf.BaseDir,
	}
	if sf.UserInvocable != nil {
		s.UserInvocable = *sf.UserInvocable
	} else {
		s.UserInvocable = true
	}
	s.Gate = DefaultGateFor(sf.Name)
	return s
}

// LoadExternalSkillsFromPaths loads skills from multiple directories in order.
// Later directories override earlier ones if names conflict.
func LoadExternalSkillsFromPaths(dirs ...string) ([]*Skill, error) {
	seen := make(map[string]int)
	var skills []*Skill
	for _, dir := range dirs {
		loaded, err := LoadExternalSkills(dir)
		if err != nil {
			return nil, err
		}
		for _, s := range loaded {
			if idx, ok := seen[s.Name]; ok {
				skills[idx] = s // override
			} else {
				seen[s.Name] = len(skills)
				skills = append(skills, s)
			}
		}
	}
	return skills, nil
}
```

关键变化：
- 移除 `strings` 和 `github.com/BurntSushi/toml` 导入
- `SkillFile` 移除所有 `toml:` 标签和 `Gate` 字段，新增 15 个字段
- `LoadExternalSkills` 仅扫描 `<name>/SKILL.md` 子目录，移除扁平文件支持
- `skillFromSkillFile` 重命名为 `SkillFromSkillFile`（导出），复制全部新字段
- `UserInvocable` 为 `*bool`，nil 时默认 true
- 移除 `parseSkillFile` 函数
- `Gate` 仅由 `DefaultGateFor` 提供

- [ ] **步骤 2：验证编译**

运行：`cd /Users/admin/gitspace/deepact && go build ./skill/`
预期：编译失败，markdown.go 中的 frontmatter 解析尚未更新（将在任务 3 修复）

---

### 任务 3：增强 Markdown 解析器（skill/markdown.go）

**文件：**
- 修改：`skill/markdown.go`

- [ ] **步骤 1：重写 frontmatter 解析段，新增辅助函数**

将 `skill/markdown.go` 中的 `ParseMarkdownSkill` 函数替换为以下版本（保留 `parsePlainMarkdownSkill`、`IsMarkdownSkill`、`stripYAMLQuotes`、`parseYAMLInlineList` 不变，新增 `parseYAMLBool`、`assignField`、`assignListField`）：

```go
// ParseMarkdownSkill parses a Markdown skill file with YAML frontmatter
// into a SkillFile. The frontmatter is delimited by --- lines and may
// contain all Claude Code-compatible fields.
// The Markdown body after the frontmatter becomes the Content.
func ParseMarkdownSkill(data []byte) (SkillFile, error) {
	// Normalize line endings to \n
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimLeft(text, "\n\t \uFEFF")

	if !strings.HasPrefix(text, "---") {
		// No YAML frontmatter - treat as plain markdown (Claude Code
		// SKILL.md files may omit frontmatter entirely).
		return parsePlainMarkdownSkill(text), nil
	}

	// Skip the opening --- delimiter and any newline after it
	rest := text[3:]
	rest = strings.TrimLeft(rest, "\n")

	// Find the closing --- delimiter (first line that is exactly ---)
	lines := strings.Split(rest, "\n")
	closeLine := -1
	for i, line := range lines {
		if line == "---" {
			closeLine = i
			break
		}
	}
	if closeLine < 0 {
		return SkillFile{}, fmt.Errorf("markdown skill file missing closing --- frontmatter delimiter")
	}

	frontmatter := strings.Join(lines[:closeLine], "\n")
	body := strings.TrimLeft(strings.Join(lines[closeLine+1:], "\n"), "\n")

	// Parse frontmatter key-value pairs
	var sf SkillFile
	fmLines := strings.Split(frontmatter, "\n")

	// Fields that accept YAML lists (inline or multi-line)
	listFields := map[string]bool{
		"keywords":      true,
		"next_skills":   true,
		"allowed-tools": true,
		"arguments":     true,
		"paths":         true,
	}

	for i := 0; i < len(fmLines); i++ {
		line := strings.TrimSpace(fmLines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])

		// Handle list fields (multi-line or inline)
		if listFields[key] {
			if value == "" {
				// Multi-line YAML list: collect following "- item" lines
				var items []string
				for j := i + 1; j < len(fmLines); j++ {
					nextLine := strings.TrimSpace(fmLines[j])
					if strings.HasPrefix(nextLine, "- ") {
						item := strings.TrimSpace(nextLine[2:])
						item = stripYAMLQuotes(item)
						items = append(items, item)
						i = j
					} else if nextLine == "" {
						continue
					} else {
						break
					}
				}
				assignListField(&sf, key, items)
			} else {
				assignListField(&sf, key, parseYAMLInlineList(value))
			}
			continue
		}

		value = stripYAMLQuotes(value)
		assignField(&sf, key, value)
	}

	sf.Content = body
	return sf, nil
}

// assignField assigns a scalar value to the matching SkillFile field.
func assignField(sf *SkillFile, key, value string) {
	switch key {
	case "name":
		sf.Name = value
	case "description":
		sf.Description = value
	case "when_to_use":
		sf.WhenToUse = value
	case "model":
		sf.Model = value
	case "effort":
		sf.Effort = value
	case "agent":
		sf.Agent = value
	case "context":
		sf.Context = value
	case "hooks":
		sf.Hooks = value
	case "version":
		sf.Version = value
	case "shell":
		sf.Shell = value
	case "argument-hint":
		sf.ArgumentHint = value
	case "user-invocable":
		b := parseYAMLBool(value)
		sf.UserInvocable = &b
	case "disable-model-invocation":
		sf.DisableModelInvocation = parseYAMLBool(value)
	}
}

// assignListField assigns a list value to the matching SkillFile field.
func assignListField(sf *SkillFile, key string, items []string) {
	switch key {
	case "keywords":
		sf.Keywords = items
	case "next_skills":
		sf.NextSkills = items
	case "allowed-tools":
		sf.AllowedTools = items
	case "arguments":
		sf.Arguments = items
	case "paths":
		sf.Paths = items
	}
}

// parseYAMLBool parses a YAML boolean value.
func parseYAMLBool(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "true" || s == "yes" || s == "1"
}
```

关键变化：
- 前置解析器从 `for range` 改为索引循环 `for i := 0`，支持多行列表回溯
- 新增 `listFields` 映射，统一处理 5 个列表字段的多行和内联格式
- 新增 `assignField` 处理 13 个标量字段（含 2 个布尔字段）
- 新增 `assignListField` 处理 5 个列表字段
- 新增 `parseYAMLBool` 解析 YAML 布尔值
- `parsePlainMarkdownSkill`、`IsMarkdownSkill`、`stripYAMLQuotes`、`parseYAMLInlineList` 保持不变

- [ ] **步骤 2：验证编译**

运行：`cd /Users/admin/gitspace/deepact && go build ./skill/`
预期：PASS

- [ ] **步骤 3：运行现有测试**

运行：`cd /Users/admin/gitspace/deepact && go test ./skill/ -run TestParseMarkdown -v`
预期：部分 FAIL（loader_test.go 中的 TOML 测试和扁平文件测试将在任务 6 修复）

---

### 任务 4：更新 skill_install 工具（tools/builtin/skill_install.go）

**文件：**
- 修改：`tools/builtin/skill_install.go`

- [ ] **步骤 1：重写 skill_install.go**

将 `tools/builtin/skill_install.go` 全文替换为：

```go
package builtin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deepact/deepact/skill"
	"github.com/deepact/deepact/tools"
)

// DefaultSkillRegistry is the URL base for fetching community skills.
// Skills are fetched from <registry>/<name>.md
const DefaultSkillRegistry = "https://raw.githubusercontent.com/deepact/skills/main"

// SkillInstallTool allows the LLM to install skills from a registry.
type SkillInstallTool struct {
	skillsDir string // e.g., ~/.deepact/skills/
	registry  *skill.Registry
	client    *http.Client
}

func NewSkillInstallTool(skillsDir string, reg *skill.Registry) *SkillInstallTool {
	return &SkillInstallTool{
		skillsDir: skillsDir,
		registry:  reg,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (t *SkillInstallTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        "skill_install",
		Description: "Install a skill from the community registry. Fetches the skill definition by name and saves it to ~/.deepact/skills/<name>/SKILL.md. After installation, the skill is immediately available. Optionally provide a custom source_url to install from a specific skill file URL.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Skill name to install (e.g., 'brainstorming', 'debugging')"},"source_url":{"type":"string","description":"Optional: custom URL to a skill file (.md with YAML frontmatter). If omitted, fetches from the default community registry."}},"required":["name"]}`),
	}
}

type skillInstallInput struct {
	Name      string `json:"name"`
	SourceURL string `json:"source_url"`
}

func (t *SkillInstallTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload skillInstallInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	payload.Name = strings.TrimSpace(payload.Name)
	if payload.Name == "" {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: "skill name is required"}, fmt.Errorf("skill name is required")
	}

	// Determine source URL
	sourceURL := payload.SourceURL
	if sourceURL == "" {
		sourceURL = fmt.Sprintf("%s/%s.md", DefaultSkillRegistry, payload.Name)
	}

	// Fetch the skill file
	resp, err := t.client.Get(sourceURL)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("fetch failed: %v", err)}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return tools.ToolResultEnvelope{
			Status: tools.StatusError,
			Digest: fmt.Sprintf("fetch failed: HTTP %d - skill '%s' not found at %s", resp.StatusCode, payload.Name, sourceURL),
		}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB max
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("read response: %v", err)}, err
	}

	// Parse as Markdown (YAML frontmatter)
	sf, err := skill.ParseMarkdownSkill(body)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid skill file: %v", err)}, err
	}
	if sf.Name == "" {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: "skill file has no name field"}, fmt.Errorf("no name field")
	}

	// Create skill directory: ~/.deepact/skills/<name>/
	skillDir := filepath.Join(t.skillsDir, payload.Name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("create skill dir: %v", err)}, err
	}

	// Write to ~/.deepact/skills/<name>/SKILL.md
	targetPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(targetPath, body, 0644); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("write skill file: %v", err)}, err
	}

	// Register in the running registry (overrides embedded if name matches)
	sf.BaseDir = skillDir
	registered := skill.SkillFromSkillFile(sf)
	t.registry.Register(registered)

	digest := fmt.Sprintf("✅ Skill '%s' installed to %s\n   Description: %s", sf.Name, targetPath, sf.Description)
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: digest}, nil
}

// compile-time check: SkillInstallTool implements Tool
var _ tools.Tool = (*SkillInstallTool)(nil)
```

关键变化：
- 移除 `"github.com/BurntSushi/toml"` 导入
- registry URL 从 `.toml` 改为 `.md`
- 移除 `isMarkdownSkill` 函数和 TOML 解析路径
- 保存路径从 `~/.deepact/skills/<name>.<ext>` 改为 `~/.deepact/skills/<name>/SKILL.md`
- 使用 `skill.SkillFromSkillFile(sf)` 替代手动创建 Skill 结构体
- Spec() 描述更新为 MD-only

- [ ] **步骤 2：验证编译**

运行：`cd /Users/admin/gitspace/deepact && go build ./tools/builtin/`
预期：PASS

---

### 任务 5：更新 buildSkillsBlock（cmd/run.go）

**文件：**
- 修改：`cmd/run.go:140-171`

- [ ] **步骤 1：更新 buildSkillsBlock 函数**

将 `cmd/run.go` 中 `buildSkillsBlock` 函数替换为：

```go
// buildSkillsBlock renders a static skills list for the stable zone.
// Each skill is shown as "name: description". The model uses semantic
// understanding (not keyword matching) to decide when to call activate_skill.
// Engine-level auto-activation is handled separately by SemanticMatcher.
// Skills with DisableModelInvocation=true are excluded (model can't auto-activate).
func buildSkillsBlock(all []*skill.Skill) string {
	if len(all) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Available Skills\n")
	b.WriteString("BLOCKING REQUIREMENT: when the user's request semantically matches a skill below, call the `activate_skill` tool to activate it BEFORE generating any other response about the task. Do not merely mention a skill by name - invoke it. If no skill matches, respond normally.\n")
	b.WriteString("Type `/<skillname>` (e.g., `/brainstorming`) to activate a specific skill explicitly. ")
	b.WriteString("Use `activate_skill` to switch skills when the current one reaches its terminal state.\n\n")
	for _, s := range all {
		if s.DisableModelInvocation {
			continue
		}
		b.WriteString("- **")
		b.WriteString(s.Name)
		b.WriteString("**: ")
		if s.Description != "" {
			b.WriteString(s.Description)
		} else {
			b.WriteString("(no description)")
		}
		if s.WhenToUse != "" {
			b.WriteString(" Use when ")
			b.WriteString(s.WhenToUse)
		}
		b.WriteString("\n")

		// Next skills in chain - LLM uses this to know what to activate next
		if len(s.NextSkills) > 0 && !(len(s.NextSkills) == 1 && s.NextSkills[0] == "") {
			b.WriteString("  -> Next: ")
			b.WriteString(strings.Join(s.NextSkills, ", "))
			b.WriteString("\n")
		}

		b.WriteString("\n")
	}
	return b.String()
}
```

关键变化：
- 新增 `if s.DisableModelInvocation { continue }` 过滤
- 新增 `if s.WhenToUse != ""` 块，追加 " Use when " + WhenToUse

- [ ] **步骤 2：验证编译**

运行：`cd /Users/admin/gitspace/deepact && go build ./cmd/`
预期：PASS

---

### 任务 6：更新测试

**文件：**
- 修改：`skill/loader_test.go`
- 修改：`skill/markdown_test.go`

- [ ] **步骤 1：重写 loader_test.go**

将 `skill/loader_test.go` 全文替换为：

```go
package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExternalSkills(t *testing.T) {
	// Directory format: <name>/SKILL.md
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "test_skill")
	os.MkdirAll(skillDir, 0o755)
	skillContent := `---
name: test_skill
description: "A test skill for unit testing"
keywords: ["test", "demo"]
---

You are a test agent.
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644)

	skills, err := LoadExternalSkills(dir)
	if err != nil {
		t.Fatalf("LoadExternalSkills failed: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "test_skill" {
		t.Errorf("name = %q", skills[0].Name)
	}
	if skills[0].Description != "A test skill for unit testing" {
		t.Errorf("description = %q", skills[0].Description)
	}
	if skills[0].BaseDir != skillDir {
		t.Errorf("BaseDir = %q, want %q", skills[0].BaseDir, skillDir)
	}
}

func TestLoadExternalSkills_SkipsReferencesDir(t *testing.T) {
	dir := t.TempDir()

	// Create a references/<skill-name>/ directory with non-skill files
	refDir := filepath.Join(dir, "references", "some-skill")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "table.md"), []byte("# reference data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a real skill in directory format
	skillDir := filepath.Join(dir, "real_skill")
	os.MkdirAll(skillDir, 0o755)
	skillContent := `---
name: real_skill
description: "A real skill"
---

You are a real agent.
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644)

	skills, err := LoadExternalSkills(dir)
	if err != nil {
		t.Fatalf("LoadExternalSkills failed: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d: %+v", len(skills), skills)
	}
	if skills[0].Name != "real_skill" {
		t.Errorf("expected real_skill, got %s", skills[0].Name)
	}
}

func TestLoadExternalSkills_DirNameFallback(t *testing.T) {
	// SKILL.md without frontmatter; name should fall back to directory name.
	dir := t.TempDir()

	nestedDir := filepath.Join(dir, "smart-test")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillContent := `# Smart Test

## Description
A skill without frontmatter.

## Prompt
Do things.
`
	if err := os.WriteFile(filepath.Join(nestedDir, "SKILL.md"), []byte(skillContent), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, err := LoadExternalSkills(dir)
	if err != nil {
		t.Fatalf("LoadExternalSkills failed: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d: %+v", len(skills), skills)
	}
	if skills[0].Name != "smart-test" {
		t.Errorf("expected name 'smart-test' (dir fallback), got %q", skills[0].Name)
	}
	if skills[0].Description != "A skill without frontmatter." {
		t.Errorf("description = %q", skills[0].Description)
	}
}

func TestLoadExternalSkillsNested(t *testing.T) {
	// Nested SKILL.md with frontmatter (Claude Code layout)
	dir := t.TempDir()
	skillContent := `---
name: nested_skill
description: "A skill in a nested SKILL.md file"
---

You are a nested test agent.
`
	nestedDir := filepath.Join(dir, "nested_skill")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "SKILL.md"), []byte(skillContent), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, err := LoadExternalSkills(dir)
	if err != nil {
		t.Fatalf("LoadExternalSkills failed: %v", err)
	}
	var found *Skill
	for _, s := range skills {
		if s.Name == "nested_skill" {
			found = s
		}
	}
	if found == nil {
		t.Fatal("nested_skill not loaded")
	}
	if found.Description != "A skill in a nested SKILL.md file" {
		t.Errorf("unexpected description: %q", found.Description)
	}
	if found.Content == "" {
		t.Error("expected non-empty content")
	}
}

func TestLoadExternalSkills_NewFields(t *testing.T) {
	// Test that new Claude Code frontmatter fields are loaded.
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "full-skill")
	os.MkdirAll(skillDir, 0o755)
	skillContent := `---
name: full-skill
description: "A skill with all fields"
allowed-tools:
  - read
  - grep
argument-hint: "[target]"
arguments:
  - target
when_to_use: "Use when you need to analyze code."
model: flash
effort: high
agent: sub
context: fork
version: "1.0"
user-invocable: false
disable-model-invocation: true
paths:
  - "src/**/*.go"
---

Do the thing.
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644)

	skills, err := LoadExternalSkills(dir)
	if err != nil {
		t.Fatalf("LoadExternalSkills failed: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if len(s.AllowedTools) != 2 || s.AllowedTools[0] != "read" || s.AllowedTools[1] != "grep" {
		t.Errorf("AllowedTools = %v", s.AllowedTools)
	}
	if s.ArgumentHint != "[target]" {
		t.Errorf("ArgumentHint = %q", s.ArgumentHint)
	}
	if len(s.Arguments) != 1 || s.Arguments[0] != "target" {
		t.Errorf("Arguments = %v", s.Arguments)
	}
	if s.WhenToUse != "Use when you need to analyze code." {
		t.Errorf("WhenToUse = %q", s.WhenToUse)
	}
	if s.Model != "flash" {
		t.Errorf("Model = %q", s.Model)
	}
	if s.Effort != "high" {
		t.Errorf("Effort = %q", s.Effort)
	}
	if s.Agent != "sub" {
		t.Errorf("Agent = %q", s.Agent)
	}
	if s.Context != "fork" {
		t.Errorf("Context = %q", s.Context)
	}
	if s.Version != "1.0" {
		t.Errorf("Version = %q", s.Version)
	}
	if s.UserInvocable != false {
		t.Errorf("UserInvocable = %v, want false", s.UserInvocable)
	}
	if !s.DisableModelInvocation {
		t.Errorf("DisableModelInvocation = false, want true")
	}
	if len(s.Paths) != 1 || s.Paths[0] != "src/**/*.go" {
		t.Errorf("Paths = %v", s.Paths)
	}
}
```

关键变化：
- `TestLoadExternalSkills`：TOML 扁平文件改为 `<name>/SKILL.md` 目录格式，新增 BaseDir 断言
- `TestLoadExternalSkills_SkipsReferencesDir`：TOML 扁平文件改为目录格式
- `TestLoadExternalSkills_DirNameFallback` 和 `TestLoadExternalSkillsNested`：保持不变
- 移除 `TestLoadExternalSkills_Markdown`（扁平 .md 不再支持）
- 移除 `TestLoadExternalSkills_MixedFormats`（TOML 不再支持）
- 新增 `TestLoadExternalSkills_NewFields`：测试全部新字段的加载

- [ ] **步骤 2：更新 markdown_test.go**

在 `skill/markdown_test.go` 末尾新增以下测试函数（保留现有测试不变）：

```go
func TestParseMarkdownSkill_NewFields(t *testing.T) {
	input := `---
name: full-skill
description: "A skill with all fields"
allowed-tools:
  - read
  - grep
argument-hint: "[target]"
arguments:
  - target
when_to_use: "Use when you need to analyze code."
model: flash
effort: high
agent: sub
context: fork
version: "1.0"
user-invocable: false
disable-model-invocation: true
paths:
  - "src/**/*.go"
---

# Full Skill

Do the thing.
`
	sf, err := ParseMarkdownSkill([]byte(input))
	if err != nil {
		t.Fatalf("ParseMarkdownSkill failed: %v", err)
	}
	if sf.Name != "full-skill" {
		t.Errorf("name = %q", sf.Name)
	}
	if len(sf.AllowedTools) != 2 || sf.AllowedTools[0] != "read" || sf.AllowedTools[1] != "grep" {
		t.Errorf("AllowedTools = %v", sf.AllowedTools)
	}
	if sf.ArgumentHint != "[target]" {
		t.Errorf("ArgumentHint = %q", sf.ArgumentHint)
	}
	if len(sf.Arguments) != 1 || sf.Arguments[0] != "target" {
		t.Errorf("Arguments = %v", sf.Arguments)
	}
	if sf.WhenToUse != "Use when you need to analyze code." {
		t.Errorf("WhenToUse = %q", sf.WhenToUse)
	}
	if sf.Model != "flash" {
		t.Errorf("Model = %q", sf.Model)
	}
	if sf.Effort != "high" {
		t.Errorf("Effort = %q", sf.Effort)
	}
	if sf.Agent != "sub" {
		t.Errorf("Agent = %q", sf.Agent)
	}
	if sf.Context != "fork" {
		t.Errorf("Context = %q", sf.Context)
	}
	if sf.Version != "1.0" {
		t.Errorf("Version = %q", sf.Version)
	}
	if sf.UserInvocable == nil || *sf.UserInvocable != false {
		t.Errorf("UserInvocable = %v, want false", sf.UserInvocable)
	}
	if !sf.DisableModelInvocation {
		t.Errorf("DisableModelInvocation = false, want true")
	}
	if len(sf.Paths) != 1 || sf.Paths[0] != "src/**/*.go" {
		t.Errorf("Paths = %v", sf.Paths)
	}
}

func TestParseMarkdownSkill_MultiLineList(t *testing.T) {
	input := `---
name: list-test
keywords:
  - alpha
  - beta
  - gamma
next_skills:
  - skill-a
  - skill-b
---

Body.
`
	sf, err := ParseMarkdownSkill([]byte(input))
	if err != nil {
		t.Fatalf("ParseMarkdownSkill failed: %v", err)
	}
	if len(sf.Keywords) != 3 || sf.Keywords[0] != "alpha" || sf.Keywords[2] != "gamma" {
		t.Errorf("keywords = %v", sf.Keywords)
	}
	if len(sf.NextSkills) != 2 || sf.NextSkills[0] != "skill-a" || sf.NextSkills[1] != "skill-b" {
		t.Errorf("next_skills = %v", sf.NextSkills)
	}
}

func TestParseMarkdownSkill_BooleanDefaults(t *testing.T) {
	// When user-invocable and disable-model-invocation are absent,
	// UserInvocable should be nil (defaults to true in SkillFromSkillFile)
	// and DisableModelInvocation should be false.
	input := `---
name: defaults-test
description: "No boolean fields"
---

Body.
`
	sf, err := ParseMarkdownSkill([]byte(input))
	if err != nil {
		t.Fatalf("ParseMarkdownSkill failed: %v", err)
	}
	if sf.UserInvocable != nil {
		t.Errorf("UserInvocable = %v, want nil (default true)", sf.UserInvocable)
	}
	if sf.DisableModelInvocation {
		t.Errorf("DisableModelInvocation = true, want false")
	}

	// Verify SkillFromSkillFile applies defaults
	s := SkillFromSkillFile(sf)
	if !s.UserInvocable {
		t.Errorf("Skill.UserInvocable = false, want true (default)")
	}
}

func TestParseMarkdownSkill_InlineListNewFields(t *testing.T) {
	input := `---
name: inline-test
allowed-tools: ["read", "grep", "bash"]
arguments: [arg1, arg2]
paths: ["src/*.go", "test/*.go"]
---

Body.
`
	sf, err := ParseMarkdownSkill([]byte(input))
	if err != nil {
		t.Fatalf("ParseMarkdownSkill failed: %v", err)
	}
	if len(sf.AllowedTools) != 3 || sf.AllowedTools[2] != "bash" {
		t.Errorf("AllowedTools = %v", sf.AllowedTools)
	}
	if len(sf.Arguments) != 2 || sf.Arguments[1] != "arg2" {
		t.Errorf("Arguments = %v", sf.Arguments)
	}
	if len(sf.Paths) != 2 || sf.Paths[0] != "src/*.go" {
		t.Errorf("Paths = %v", sf.Paths)
	}
}
```

同时移除 `TestLoadExternalSkills_Markdown` 和 `TestLoadExternalSkills_MixedFormats`（在 markdown_test.go 中的副本，行 131-196），因为扁平文件不再被 loader 支持。

- [ ] **步骤 3：运行全部 skill 包测试**

运行：`cd /Users/admin/gitspace/deepact && go test ./skill/ -v`
预期：全部 PASS

---

### 任务 7：全量构建和测试验证

**文件：** 无（验证步骤）

- [ ] **步骤 1：全量构建**

运行：`cd /Users/admin/gitspace/deepact && go build ./...`
预期：PASS

- [ ] **步骤 2：运行 skill 包测试**

运行：`cd /Users/admin/gitspace/deepact && go test ./skill/ -v`
预期：全部 PASS

- [ ] **步骤 3：运行 tools/builtin 包测试**

运行：`cd /Users/admin/gitspace/deepact && go test ./tools/builtin/ -v`
预期：PASS

- [ ] **步骤 4：运行 cmd 包测试**

运行：`cd /Users/admin/gitspace/deepact && go test ./cmd/ -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
cd /Users/admin/gitspace/deepact
git add skill/skill.go skill/loader.go skill/markdown.go skill/loader_test.go skill/markdown_test.go tools/builtin/skill_install.go cmd/run.go docs/superpowers/specs/2026-07-23-skill-folder-management-design.md docs/superpowers/plans/2026-07-23-skill-folder-management.md
git commit -m "feat: align skill folder management with Claude Code

- Remove TOML support entirely; only <name>/SKILL.md directory format
- Add 15 Claude Code-compatible frontmatter fields to Skill/SkillFile:
  allowed-tools, argument-hint, arguments, when_to_use, model, effort,
  agent, context, hooks, paths, user-invocable, disable-model-invocation,
  version, shell, base_dir
- Enhance markdown parser: multi-line YAML lists, boolean parsing
- skill_install: save as <name>/SKILL.md directory format, fetch .md
- buildSkillsBlock: filter disable-model-invocation, show when_to_use
- Gates provided solely by gates.go defaults (no gate in skill files)"
```
