# AGENTS.md 加载实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 启动时读取 `~/.deepact/AGENTS.md`（用户级）与 `<projectRoot>/AGENTS.md`（项目级），级联注入稳定区（Block S）。

**架构：** 新增 `context/agents.go` 提供 `LoadAgentsMD`（读两个位置，级联叠加，项目级在后）与 `RenderAgentsBlock`（渲染为稳定区块）。`ContextAssembler` 增加 `agentsBlock` 字段与 `SetAgentsBlock`，`Build()` 时拼入 `stableSessionBlock`。`cmd/run.go` 的 `buildEngineDeps` 加载并注入。

**技术栈：** Go，标准库（os/path/filepath/strings），既有 context 包与 `BuildStableSessionContext`。

---

### 任务 1：context/agents.go — 加载与渲染

**文件：**
- 创建：`context/agents.go`
- 测试：`context/agents_test.go`

- [ ] **步骤 1：编写失败的测试**

创建 `context/agents_test.go`：

```go
package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadAgentsMD_Cascade verifies user-level + project-level both load,
// project-level placed after user-level (higher priority / closer to task).
func TestLoadAgentsMD_Cascade(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	t.Setenv("HOME", home)

	userContent := "# 全局规则\n不要提交 .env"
	projContent := "# 项目规则\n用标准库"

	if err := os.MkdirAll(filepath.Join(home, ".deepact"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".deepact", "AGENTS.md"), []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte(projContent), 0o644); err != nil {
		t.Fatal(err)
	}

	files := LoadAgentsMD(proj)
	if len(files) != 2 {
		t.Fatalf("len(files) = %d, want 2", len(files))
	}
	// 用户级在前，项目级在后
	if files[0].Source != "~/.deepact/AGENTS.md" {
		t.Errorf("files[0].Source = %q, want ~/.deepact/AGENTS.md", files[0].Source)
	}
	if !strings.Contains(files[0].Content, "全局规则") {
		t.Errorf("files[0].Content = %q, want contains 全局规则", files[0].Content)
	}
	if files[1].Source != "AGENTS.md" {
		t.Errorf("files[1].Source = %q, want AGENTS.md", files[1].Source)
	}
	if !strings.Contains(files[1].Content, "项目规则") {
		t.Errorf("files[1].Content = %q, want contains 项目规则", files[1].Content)
	}
}

// TestLoadAgentsMD_UserMissing verifies project-level alone works.
func TestLoadAgentsMD_UserMissing(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("# 项目规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := LoadAgentsMD(proj)
	if len(files) != 1 || files[0].Source != "AGENTS.md" {
		t.Fatalf("files = %+v, want 1 project-only file", files)
	}
}

// TestLoadAgentsMD_ProjectMissing verifies user-level alone works.
func TestLoadAgentsMD_ProjectMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".deepact"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".deepact", "AGENTS.md"), []byte("# 全局规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := LoadAgentsMD(t.TempDir())
	if len(files) != 1 || files[0].Source != "~/.deepact/AGENTS.md" {
		t.Fatalf("files = %+v, want 1 user-only file", files)
	}
}

// TestLoadAgentsMD_None verifies empty result when neither exists.
func TestLoadAgentsMD_None(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	files := LoadAgentsMD(t.TempDir())
	if len(files) != 0 {
		t.Fatalf("len(files) = %d, want 0", len(files))
	}
}

// TestLoadAgentsMD_EmptyFile verifies empty files are skipped.
func TestLoadAgentsMD_EmptyFile(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".deepact"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".deepact", "AGENTS.md"), []byte("   \n\n  "), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("# 项目规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := LoadAgentsMD(proj)
	if len(files) != 1 || files[0].Source != "AGENTS.md" {
		t.Fatalf("files = %+v, want only project file (empty user skipped)", files)
	}
}

// TestRenderAgentsBlock verifies rendering shape: empty input -> empty output.
func TestRenderAgentsBlock(t *testing.T) {
	if got := RenderAgentsBlock(nil); got != "" {
		t.Errorf("RenderAgentsBlock(nil) = %q, want empty", got)
	}
	files := []AgentsFile{
		{Source: "~/.deepact/AGENTS.md", Content: "全局规则"},
		{Source: "AGENTS.md", Content: "项目规则"},
	}
	got := RenderAgentsBlock(files)
	for _, want := range []string{"Project Conventions", "全局规则", "项目规则", "AGENTS.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderAgentsBlock() missing %q in:\n%s", want, got)
		}
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./context/ -run TestLoadAgentsMD -v`
预期：FAIL（编译错误，`LoadAgentsMD` / `RenderAgentsBlock` / `AgentsFile` 未定义）

- [ ] **步骤 3：编写实现代码**

创建 `context/agents.go`：

```go
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
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./context/ -run TestLoadAgentsMD -v && go test ./context/ -run TestRenderAgentsBlock -v`
预期：全部 PASS

- [ ] **步骤 5：Commit**

```bash
git add context/agents.go context/agents_test.go
git commit -m "feat(context): load AGENTS.md files (user + project level)"
```

---

### 任务 2：context/builder.go — 注入稳定区

**文件：**
- 修改：`context/builder.go:24-26`（新增字段）
- 修改：`context/builder.go:47`（`SetSkillsBlock` 之后新增 `SetAgentsBlock`）
- 修改：`context/builder.go:100-102`（拼接 stableSessionBlock）
- 测试：`context/agents_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

在 `context/agents_test.go` 末尾追加：

```go
// TestSetAgentsBlock verifies agents block is stored and cleared.
func TestSetAgentsBlock(t *testing.T) {
	a := NewContextAssembler(t.TempDir(), nil)
	a.SetAgentsBlock("\n## Project Conventions (AGENTS.md)\n\n# 规则\n")
	if a.agentsBlock == "" {
		t.Fatal("agentsBlock not stored")
	}
	// 空内容清空
	a.SetAgentsBlock("")
	if a.agentsBlock != "" {
		t.Fatalf("agentsBlock = %q, want empty", a.agentsBlock)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./context/ -run TestSetAgentsBlock -v`
预期：FAIL（编译错误，`agentsBlock` 字段不存在）

- [ ] **步骤 3：编写实现代码**

在 `context/builder.go` 的 `ContextAssembler` 结构体（第 24 行 `activeSkillBlock` 字段之后）增加：

```go
	agentsBlock        string          // AGENTS.md 内容（启动时构建，stable zone 用，缓存）
```

在 `SetSkillsBlock` 方法（第 47 行附近）之后新增：

```go
// SetAgentsBlock sets the rendered AGENTS.md content for inclusion in the
// stable zone. Called once at startup from cmd/run.go after LoadAgentsMD.
// Empty string means no AGENTS.md files were found (output identical to current).
func (a *ContextAssembler) SetAgentsBlock(rendered string) {
	a.agentsBlock = rendered
}
```

在 `Build()` 中 `stableSessionBlock` 构建处（第 100-102 行）修改：

```go
	if a.stableSessionBlock == "" && a.userLangSet {
		a.stableSessionBlock = BuildStableSessionContext(a.envInfo, a.userLang)
		if a.agentsBlock != "" {
			a.stableSessionBlock += a.agentsBlock
		}
	}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./context/ -v`
预期：全部 PASS（含既有测试）

- [ ] **步骤 5：Commit**

```bash
git add context/builder.go context/agents_test.go
git commit -m "feat(context): inject AGENTS.md into stable session block"
```

---

### 任务 3：cmd/run.go — 启动接线

**文件：**
- 修改：`cmd/run.go:295`（`SetSkillsBlock` 调用之后）

- [ ] **步骤 1：编写实现代码**

在 `cmd/run.go` 的 `buildEngineDeps` 中，`contextAssembler.SetSkillsBlock(skillsBlock)`（第 295 行）之后追加：

```go
	// Load AGENTS.md files (user-level + project-level) into the stable zone.
	// Missing files are skipped; empty block → output identical to current.
	agentsFiles := deeplogcontext.LoadAgentsMD(workDir)
	contextAssembler.SetAgentsBlock(deeplogcontext.RenderAgentsBlock(agentsFiles))
```

- [ ] **步骤 2：编译验证**

运行：`go build ./...`
预期：无错误

- [ ] **步骤 3：运行相关测试**

运行：`go test ./context/ ./cmd/`
预期：全部 PASS

- [ ] **步骤 4：Commit**

```bash
git add cmd/run.go
git commit -m "feat(cmd): load AGENTS.md into context at startup"
```

---

## 自检

- **规格覆盖度：** 规格 A2 各需求——位置（项目根+用户级）→ 任务 1；级联叠加（项目级在后）→ 任务 1 测试断言；注入稳定区 → 任务 2；启动接线 → 任务 3；错误处理（缺失静默/零回归）→ 任务 1 `readAgentsMD` + `RenderAgentsBlock` 空输出；测试计划 → 任务 1/2。全覆盖。
- **占位符扫描：** 无 TODO/待定；所有代码块完整。
- **类型一致性：** `AgentsFile{Source,Content}`、`LoadAgentsMD(projectRoot)`、`RenderAgentsBlock(files)`、`SetAgentsBlock(rendered)`、`agentsBlock` 在各任务间一致。
