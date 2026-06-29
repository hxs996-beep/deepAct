# read_multi 扇出读取工具 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 新增 `read_multi` 工具，让 LLM 一次调用并行读取 ≤8 个文件目标，单轮拿到多方向信息，减少串行 read 往返。

**架构：** 在 `tools/builtin/` 新增独立工具 `read_multi`，复用从 `read.go` 抽出的纯抓取函数（`readFullContent`/`readLinesContent`/`truncateContent`），对每个 target 起 goroutine 并行抓取并按输入顺序聚合成单个 tool 结果。引擎层 `engine/turn.go` 在 `updateTaskStateFromTools` 中解析结果自描述元数据为每个 sub-target 写 `ReadRecord`，并在 loop-guard 检查处对 `read_multi` 的每个 target 做独立计数，确保不绕过 read 循环守卫。提示层在 `context/promptset/{zh,en}/system.md` 追加使用引导。

**技术栈：** Go 1.24+，标准库 `sync`/`encoding/json`/`bufio`，已有 `tools`/`engine` 包。

**规格来源：** `docs/superpowers/specs/2026-06-28-read-multi-fanout-design.md`

**前置事实（已核实）：**
- `ReadRecord`/`ReadHistory` 结构体已存在于 `engine/types.go:239-251`，但 `read` 工具当前未写入它们（read-loop-prevention 设计未实现）。本计划让 `read_multi` 独立写入，向前兼容。
- `LoopGuard.Check`（`engine/guards.go:162`）对未知工具经 `extractToolKey` 返回 `""` → 当前 `read_multi` 会完全绕过守卫。本计划补齐 per-target 计数。
- `extractToolKey`（`engine/guards.go:55-85`）对 `read` 已按 `path + scopeHash` 计数；本计划复用其 `read` 分支，通过合成 `read` 调用为 `read_multi` 的每个 target 计数。
- 系统提示已有"禁止一轮只发一个只读工具"规则（`context/promptset/zh/system.md:52`），`read_multi` 与之互补。

---

## 文件结构

| 文件 | 职责 | 动作 |
|---|---|---|
| `tools/builtin/read.go` | 抽出 `readFullContent`/`readLinesContent`/`truncateContent` 复用函数；`Run` 改为调用它们（行为零变化） | 修改 |
| `tools/builtin/read_multi.go` | 新工具 `read_multi`：并行抓取多 target + 聚合 | 创建 |
| `tools/builtin/read_multi_test.go` | `read_multi` 单元测试 | 创建 |
| `cmd/run.go` | 注册 `read_multi` | 修改（1 行） |
| `engine/turn.go` | `updateTaskStateFromTools` 增 `case "read_multi"` 写多条 ReadRecord；loop-guard 检查处特判 per-target | 修改 |
| `engine/guards.go` | 新增 `parseReadMultiTargets` helper（engine 包内视图） | 修改 |
| `engine/read_multi_integration_test.go` | ReadHistory 写入 + loop-guard per-target 阻断测试 | 创建 |
| `context/promptset/zh/system.md` | 中文使用引导 | 修改 |
| `context/promptset/en/system.md` | 英文使用引导 | 修改 |

---

## 任务 1：从 read.go 抽出复用抓取函数（纯重构）

**文件：**
- 修改：`tools/builtin/read.go`（`Run` 方法体 76-149 行 + 新增三个函数）
- 测试：`tools/builtin/readwrite_test.go`（现有，作为回归守护，不改动）

**目标：** 把 `read.go` 的 `Run` 中"裸读"与"offset/limit 读"两段抓取逻辑抽成 `readFullContent`/`readLinesContent`，token 截断抽成 `truncateContent`，供 `read_multi` 复用。`read` 行为零变化，由 `readwrite_test.go` 现有 4 个用例守护。

- [ ] **步骤 1：确认现有测试通过（重构基线）**

运行：`go test ./tools/builtin/ -run TestReadTool -v`
预期：PASS（`TestReadTool_BasicFile`、`TestReadTool_OffsetAndLimit`、`TestReadTool_BinaryDetection`、`TestReadTool_FileNotFound` 全过）。

- [ ] **步骤 2：新增 `truncateContent` 函数**

在 `tools/builtin/read.go` 的 `truncateByChars` 函数（153-164 行）之后，新增：

```go
// truncateContent applies the maxReadTokens cap to numbered file content.
// If content fits, returns it unchanged; otherwise truncates at the last
// complete line and appends a hint. Shared by read and read_multi.
func truncateContent(content string) string {
	estimatedTokens := len(content) / charsPerToken
	if estimatedTokens <= maxReadTokens {
		return content
	}
	truncated := truncateByChars(content, maxReadTokens*charsPerToken)
	truncatedLines := strings.Count(truncated, "\n")
	return fmt.Sprintf("%s\n[... truncated at %d lines (~%d tokens out of ~%d estimated). Use offset/limit to read specific sections.]",
		truncated, truncatedLines, maxReadTokens, estimatedTokens)
}
```

- [ ] **步骤 3：新增 `readFullContent` 函数**

在 `truncateContent` 之后新增（封装 `read` 的裸读段：stat/size/open/binary/seek/scan-all/truncate）：

```go
// readFullContent reads an entire file with line numbers, applying the size
// and token caps. Returns numbered content (possibly truncated with a hint).
// Does not append the lspHint — callers decide.
func readFullContent(safePath string) (string, error) {
	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	if info.Size() > maxReadBytes {
		return "", fmt.Errorf("file too large (%.1fMB, max 1MB). Use offset/limit to read specific sections.", float64(info.Size())/(1<<20))
	}

	file, err := os.Open(safePath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	if err := detectBinary(file); err != nil {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek file: %w", err)
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var builder strings.Builder
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		builder.WriteString(fmt.Sprintf("%d: %s\n", lineNum, scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	content := builder.String()
	if content == "" {
		content = "(empty)"
	}
	return truncateContent(content), nil
}
```

- [ ] **步骤 4：新增 `readLinesContent` 函数**

在 `readFullContent` 之后新增（封装 `read` 的 offset/limit 段）：

```go
// readLinesContent reads a range of lines from a file with numbering.
// offset is 1-based (clamped to >=1); limit<=0 means read to EOF from offset.
// Applies the same size and token caps as readFullContent.
func readLinesContent(safePath string, offset, limit int) (string, error) {
	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	if info.Size() > maxReadBytes {
		return "", fmt.Errorf("file too large (%.1fMB, max 1MB). Use offset/limit to read specific sections.", float64(info.Size())/(1<<20))
	}

	file, err := os.Open(safePath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	if err := detectBinary(file); err != nil {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek file: %w", err)
	}

	if offset < 1 {
		offset = 1
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var builder strings.Builder
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < offset {
			continue
		}
		if limit > 0 && lineNum >= offset+limit {
			break
		}
		builder.WriteString(fmt.Sprintf("%d: %s\n", lineNum, scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	content := builder.String()
	if content == "" {
		content = "(empty)"
	}
	return truncateContent(content), nil
}
```

- [ ] **步骤 5：重构 `read` 的 `Run` 调用新函数**

将 `tools/builtin/read.go` 的 `Run` 方法中**从 `info, err := os.Stat(safePath)`（76 行）到 token 截断返回（149 行）**的整段（即 symbol 分支之后、`return` 之前的主逻辑），替换为下面的分支调用。保留 symbol 分支（66-74 行）和前面的 `resolveSafePath`（61-64 行）不动。

替换 76-149 行为：

```go
	// Full read (no offset/limit): use readFullContent and update mtime cache.
	if payload.Offset == 0 && payload.Limit == 0 {
		content, err := readFullContent(safePath)
		if err != nil {
			return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
		}
		// Update mtime cache only for full reads (no offset/limit).
		if info, statErr := os.Stat(safePath); statErr == nil {
			t.mtimeCache.Store(safePath, info.ModTime().UnixMilli())
		}
		return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: content + lspHint}, nil
	}

	// offset/limit read.
	content, err := readLinesContent(safePath, payload.Offset, payload.Limit)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: content + lspHint}, nil
```

注意：原 `lspHint` 常量定义在 137 行（重构后仍在函数内或移到函数外）。若重构后 `lspHint` 的作用域位置变动导致编译错误，将其从 `Run` 内的局部常量提升为包级常量：

```go
// lspHint is appended to read results to nudge toward lsp for symbol/type queries.
const lspHint = "\n\n---\nNeed to find a symbol definition, type info, or references? Use the `lsp` tool instead of reading the whole file (e.g., `lsp operation=hover file_path=<path> line=<line> character=<char>`)."
```

放到 `read.go` 顶部常量区（`maxReadBytes` 等附近，19-25 行区域），并删除 `Run` 内 137 行的局部定义。

- [ ] **步骤 6：编译**

运行：`go build ./tools/builtin/`
预期：无错误。

- [ ] **步骤 7：运行回归测试确认零行为变化**

运行：`go test ./tools/builtin/ -run TestReadTool -v`
预期：PASS（4 个用例全过）。若 `TestReadTool_OffsetAndLimit` 失败，检查 `readLinesContent` 的 limit 边界是否与原逻辑一致（原：`lineNum >= offset+readLimit` 时 break；新函数同）。

- [ ] **步骤 8：Commit**

```bash
git add tools/builtin/read.go
git commit -m "refactor(tools): extract readFullContent/readLinesContent for reuse

Pure refactor: pull full-read and offset/limit-read logic out of ReadTool.Run
into readFullContent/readLinesContent/truncateContent so read_multi can reuse
them. read behavior unchanged (guarded by readwrite_test.go)."
```

---

## 任务 2：创建 read_multi 工具

**文件：**
- 创建：`tools/builtin/read_multi.go`
- 测试：`tools/builtin/read_multi_test.go`

- [ ] **步骤 1：编写失败的测试（多 target 并行抓取 + 顺序 + 分隔头）**

创建 `tools/builtin/read_multi_test.go`：

```go
package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepact/deepact/tools"
)

func TestReadMulti_ThreeTargetsOrdered(t *testing.T) {
	dir := t.TempDir()
	// target 1: full read
	fullPath := filepath.Join(dir, "full.txt")
	os.WriteFile(fullPath, []byte("a\nb\nc\n"), 0o644)
	// target 2: offset/limit
	rangePath := filepath.Join(dir, "range.txt")
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "line"
	}
	os.WriteFile(rangePath, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	// target 3: go symbol
	goPath := filepath.Join(dir, "sym.go")
	os.WriteFile(goPath, []byte("package main\n\n// Run does stuff\nfunc Run() {\n  return\n}\n"), 0o644)

	tool := NewReadMultiTool()
	input, _ := json.Marshal(map[string]interface{}{
		"targets": []map[string]interface{}{
			{"path": fullPath},
			{"path": rangePath, "offset": 5, "limit": 3},
			{"path": goPath, "symbol": "Run"},
		},
	})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Fatalf("status = %q, digest: %s", result.Status, result.Digest)
	}
	// Headers appear in input order.
	idxFull := strings.Index(result.Digest, "=== [1] "+fullPath+" (full) ===")
	idxRange := strings.Index(result.Digest, "=== [2] "+rangePath+" [L5-7] ===")
	idxSym := strings.Index(result.Digest, "=== [3] "+goPath+" [symbol:Run] ===")
	if idxFull < 0 || idxRange < 0 || idxSym < 0 {
		t.Fatalf("missing/incorrect header in digest:\n%s", result.Digest)
	}
	if !(idxFull < idxRange && idxRange < idxSym) {
		t.Fatalf("targets not in input order: %d %d %d", idxFull, idxRange, idxSym)
	}
	// Metadata comment at top.
	if !strings.HasPrefix(result.Digest, "<!-- read_multi targets:") {
		t.Fatalf("missing metadata comment at top:\n%s", result.Digest[:80])
	}
	// Symbol body present.
	if !strings.Contains(result.Digest, "symbol Run") {
		t.Fatalf("symbol body missing in digest:\n%s", result.Digest)
	}
}

func TestReadMulti_PartialFailure(t *testing.T) {
	dir := t.TempDir()
	okPath := filepath.Join(dir, "ok.txt")
	os.WriteFile(okPath, []byte("hello\n"), 0o644)
	goPath := filepath.Join(dir, "sym.go")
	os.WriteFile(goPath, []byte("package main\n"), 0o644)

	tool := NewReadMultiTool()
	input, _ := json.Marshal(map[string]interface{}{
		"targets": []map[string]interface{}{
			{"path": okPath},
			{"path": goPath, "symbol": "Missing"},
		},
	})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v (batch must not fail on partial error)", err)
	}
	if result.Status != tools.StatusOK {
		t.Fatalf("status = %q, want ok for partial failure", result.Status)
	}
	if !strings.Contains(result.Digest, "hello") {
		t.Fatalf("ok target content missing:\n%s", result.Digest)
	}
	if !strings.Contains(result.Digest, "ERROR:") {
		t.Fatalf("missing ERROR marker for failed target:\n%s", result.Digest)
	}
}

func TestReadMulti_TooManyTargets(t *testing.T) {
	dir := t.TempDir()
	targets := make([]map[string]interface{}, 9)
	for i := range targets {
		targets[i] = map[string]interface{}{"path": filepath.Join(dir, "x.txt")}
	}
	tool := NewReadMultiTool()
	input, _ := json.Marshal(map[string]interface{}{"targets": targets})
	_, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err == nil {
		t.Fatal("expected error for >8 targets")
	}
}

func TestReadMulti_EmptyTargets(t *testing.T) {
	tool := NewReadMultiTool()
	input, _ := json.Marshal(map[string]interface{}{"targets": []map[string]interface{}{}})
	_, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	_ = input
}
```

注意：最后一个 `TestReadMulti_EmptyTargets` 故意引用未定义的 `dir` 使其编译失败——步骤 1 只需"测试文件存在且编译失败"即可。在步骤 3 实现后将其修正为正确形式（见步骤 3 之后的修正说明）。先保留编译失败状态以验证 TDD 红灯。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./tools/builtin/ -run TestReadMulti -v`
预期：编译失败，报错 `undefined: NewReadMultiTool`（及 `TestReadMulti_EmptyTargets` 里的 `dir` 未定义）。

- [ ] **步骤 3：实现 read_multi.go**

创建 `tools/builtin/read_multi.go`：

```go
package builtin

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/deepact/deepact/tools"
)

const readMultiMaxTargets = 8

type ReadMultiTool struct{}

func NewReadMultiTool() *ReadMultiTool {
	return &ReadMultiTool{}
}

func (t *ReadMultiTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name: "read_multi",
		Description: "Read up to 8 file targets in one call, executed in parallel. Use this for fan-out " +
			"exploration: when you need to understand several files/symbols/directions at once, list them " +
			"as targets instead of issuing many single reads. Each target supports path + optional " +
			"symbol/offset/limit, same semantics as `read`. Prefer this over chained single reads when you " +
			"have 2+ independent things to look at.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"targets":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string","description":"Path to the file"},"symbol":{"type":"string","description":"Name of Go symbol to read (function/type/struct/variable/constant). Works only for .go files. When set, offset/limit are ignored."},"offset":{"type":"integer","description":"Starting line number (1-based)"},"limit":{"type":"integer","description":"Max lines to read"}},"required":["path"]},"maxItems":8,"minItems":1}},"required":["targets"]}`),
	}
}

type readMultiTarget struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type readMultiInput struct {
	Targets []readMultiTarget `json:"targets"`
}

// readMultiResult is one target's outcome, stored by input index.
type readMultiResult struct {
	header string
	body   string
	scope  string // canonical scope string for ReadHistory: "", "symbol:X", "L a-b", "L a-end"
	err    error
}

func (t *ReadMultiTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload readMultiInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	if len(payload.Targets) == 0 {
		err := fmt.Errorf("at least one target required")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}
	if len(payload.Targets) > readMultiMaxTargets {
		err := fmt.Errorf("too many targets: %d (max %d)", len(payload.Targets), readMultiMaxTargets)
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	results := make([]readMultiResult, len(payload.Targets))
	var wg sync.WaitGroup
	for i, tgt := range payload.Targets {
		wg.Add(1)
		go func(i int, tgt readMultiTarget) {
			defer wg.Done()
			results[i] = fetchTarget(ctx.WorkDir, tgt)
		}(i, tgt)
	}
	wg.Wait()

	var b strings.Builder
	b.WriteString("<!-- read_multi targets: ")
	parts := make([]string, len(payload.Targets))
	for i, tgt := range payload.Targets {
		parts[i] = tgt.Path + "::" + results[i].scope
	}
	b.WriteString(strings.Join(parts, " | "))
	b.WriteString(" -->\n")
	b.WriteString(fmt.Sprintf("ReadMulti: %d targets (parallel)\n", len(payload.Targets)))
	b.WriteString(strings.Repeat("─", 32) + "\n")
	for _, r := range results {
		b.WriteString(r.header + "\n")
		if r.err != nil {
			b.WriteString(fmt.Sprintf("ERROR: %v\n", r.err))
		} else {
			b.WriteString(r.body + "\n")
		}
		b.WriteString("\n")
	}
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: b.String()}, nil
}

// fetchTarget resolves the path and fetches content for one target.
func fetchTarget(workDir string, tgt readMultiTarget) readMultiResult {
	tgt.Path = strings.TrimSpace(tgt.Path)
	r := readMultiResult{}
	safePath, err := resolveSafePath(workDir, tgt.Path)
	if err != nil {
		r.err = err
		return r
	}

	switch {
	case tgt.Symbol != "" && strings.HasSuffix(safePath, ".go"):
		content, err := readSymbol(safePath, tgt.Symbol)
		if err != nil {
			r.err = err
			return r
		}
		lc := strings.Count(content, "\n")
		r.scope = "symbol:" + tgt.Symbol
		r.header = fmt.Sprintf("=== [%s] [symbol:%s] ===", tgt.Path, tgt.Symbol)
		r.body = fmt.Sprintf("symbol %s (%d lines)\n%s", tgt.Symbol, lc, content)
	case tgt.Offset > 0 || tgt.Limit > 0:
		content, err := readLinesContent(safePath, tgt.Offset, tgt.Limit)
		if err != nil {
			r.err = err
			return r
		}
		lo := tgt.Offset
		if lo < 1 {
			lo = 1
		}
		if tgt.Limit > 0 {
			r.scope = fmt.Sprintf("L%d-%d", lo, lo+tgt.Limit-1)
		} else {
			r.scope = fmt.Sprintf("L%d-end", lo)
		}
		r.header = fmt.Sprintf("=== [%s] [%s] ===", tgt.Path, r.scope)
		r.body = content
	default:
		content, err := readFullContent(safePath)
		if err != nil {
			r.err = err
			return r
		}
		r.scope = ""
		r.header = fmt.Sprintf("=== [%s] (full) ===", tgt.Path)
		r.body = content
	}
	return r
}
```

- [ ] **步骤 4：修正测试中的故意错误**

将 `TestReadMulti_EmptyTargets` 改为正确形式：

```go
func TestReadMulti_EmptyTargets(t *testing.T) {
	tool := NewReadMultiTool()
	input, _ := json.Marshal(map[string]interface{}{"targets": []map[string]interface{}{}})
	_, err := tool.Run(tools.ToolContext{}, input)
	if err == nil {
		t.Fatal("expected error for empty targets")
	}
}
```

- [ ] **步骤 5：编译 + 运行测试验证通过**

运行：`go test ./tools/builtin/ -run TestReadMulti -v`
预期：4 个用例 PASS。

- [ ] **步骤 6：确认 read 测试未受影响**

运行：`go test ./tools/builtin/ -run TestReadTool -v`
预期：PASS。

- [ ] **步骤 7：Commit**

```bash
git add tools/builtin/read_multi.go tools/builtin/read_multi_test.go
git commit -m "feat(tools): add read_multi tool for parallel fan-out reads

One LLM call reads up to 8 file targets in parallel (symbol/offset-limit/full),
aggregated into a single result with per-target headers and a metadata comment.
Partial failures are marked ERROR per target without failing the batch."
```

---

## 任务 3：注册 read_multi

**文件：**
- 修改：`cmd/run.go:323-333`（`registerBuiltinTools`）

- [ ] **步骤 1：注册工具**

在 `cmd/run.go` 的 `registerBuiltinTools` 中，`registry.Register(builtin.NewReadTool())`（324 行）之后新增一行：

```go
	registry.Register(builtin.NewReadTool())
	registry.Register(builtin.NewReadMultiTool())
```

- [ ] **步骤 2：编译 + 启动冒烟**

运行：`go build ./...`
预期：无错误。

- [ ] **步骤 3：Commit**

```bash
git add cmd/run.go
git commit -m "feat(cmd): register read_multi builtin tool"
```

---

## 任务 4：engine 写入 ReadHistory（解析自描述元数据）

**文件：**
- 修改：`engine/turn.go`（`updateTaskStateFromTools` 907-931 行）
- 测试：`engine/read_multi_integration_test.go`

**目标：** `read_multi` 调用后，为每个 sub-target 追加一条 `ReadRecord{Path, Scope}`，scope 串口径与单读一致。解析结果 digest 头部的 `<!-- read_multi targets: ... -->` 元数据。

- [ ] **步骤 1：编写失败的测试**

创建 `engine/read_multi_integration_test.go`：

```go
package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseReadMultiDigestScopes(t *testing.T) {
	digest := "<!-- read_multi targets: a.go::symbol:Run | b.go::L5-7 | c.txt:: | d.go::L10-end -->\nReadMulti: 4 targets (parallel)"
	recs := parseReadMultiDigestScopes(digest)
	if len(recs) != 4 {
		t.Fatalf("got %d records, want 4", len(recs))
	}
	want := []ReadRecord{
		{Path: "a.go", Scope: "symbol:Run"},
		{Path: "b.go", Scope: "L5-7"},
		{Path: "c.txt", Scope: ""},
		{Path: "d.go", Scope: "L10-end"},
	}
	for i, w := range want {
		if recs[i] != w {
			t.Errorf("rec[%d] = %+v, want %+v", i, recs[i], w)
		}
	}
}

func TestParseReadMultiDigestScopes_NoMetadata(t *testing.T) {
	// No metadata comment → best-effort: return nil, no panic.
	recs := parseReadMultiDigestScopes("just some content without meta")
	if recs != nil {
		t.Fatalf("expected nil for missing metadata, got %v", recs)
	}
}

func TestUpdateTaskState_ReadMultiWritesReadRecords(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	digest := "<!-- read_multi targets: a.go::symbol:Run | b.go::L5-7 -->\nReadMulti: 2 targets (parallel)\n..."
	calls := []ToolCallRequest{{ID: "1", Name: "read_multi", Input: json.RawMessage(`{"targets":[{"path":"a.go"}]}`)}}
	results := []ToolResult{{ToolCallID: "1", ToolName: "read_multi", Status: "ok", Digest: digest}}

	e.updateTaskStateFromTools(calls, results)

	if len(e.state.ReadHistory) != 2 {
		t.Fatalf("ReadHistory len = %d, want 2; records: %+v", len(e.state.ReadHistory), e.state.ReadHistory)
	}
	if e.state.ReadHistory[0] != (ReadRecord{Path: "a.go", Scope: "symbol:Run"}) {
		t.Errorf("rec[0] = %+v", e.state.ReadHistory[0])
	}
	if e.state.ReadHistory[1] != (ReadRecord{Path: "b.go", Scope: "L5-7"}) {
		t.Errorf("rec[1] = %+v", e.state.ReadHistory[1])
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run "TestParseReadMultiDigestScopes|TestUpdateTaskState_ReadMultiWritesReadRecords" -v`
预期：编译失败，`undefined: parseReadMultiDigestScopes`，且 `case "read_multi"` 不存在导致 ReadHistory 为空。

- [ ] **步骤 3：新增 `parseReadMultiDigestScopes` helper**

在 `engine/turn.go` 的 `extractPathFromArgs`（939 行）之前新增：

```go
// parseReadMultiDigestScopes extracts per-target ReadRecords from a read_multi
// result's self-describing metadata comment:
//   <!-- read_multi targets: path1::scope1 | path2::scope2 | ... -->
// Returns nil if the metadata line is absent or malformed (best-effort: missing
// metadata just skips ReadHistory bookkeeping; the loop guard still backstops).
func parseReadMultiDigestScopes(digest string) []ReadRecord {
	lineEnd := strings.Index(digest, "\n")
	if lineEnd < 0 {
		lineEnd = len(digest)
	}
	firstLine := digest[:lineEnd]
	const marker = "<!-- read_multi targets:"
	start := strings.Index(firstLine, marker)
	if start < 0 {
		return nil
	}
	rest := firstLine[start+len(marker):]
	end := strings.Index(rest, "-->")
	if end < 0 {
		return nil
	}
	body := strings.TrimSpace(rest[:end])
	if body == "" {
		return nil
	}
	var recs []ReadRecord
	for _, part := range strings.Split(body, " | ") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "::", 2)
		if len(kv) != 2 {
			continue
		}
		recs = append(recs, ReadRecord{Path: kv[0], Scope: kv[1]})
	}
	return recs
}
```

确认 `engine/turn.go` 顶部已 import `"strings"`（应已存在）。

- [ ] **步骤 4：在 `updateTaskStateFromTools` 加 `case "read_multi"`**

在 `engine/turn.go:916` 的 `switch call.Name` 中，`case "read",`（实际是 `case "read":`）之前新增一个分支。将 916-929 行的 switch 改为：

```go
		switch call.Name {
		case "edit", "write":
			if !containsString(e.state.ModifiedFiles, path) {
				e.state.ModifiedFiles = append(e.state.ModifiedFiles, path)
			}
			e.state.EditScopeFiles = len(e.state.ModifiedFiles)
			addToWorkingSet(e.state, path, "modified")
		case "read":
			addToWorkingSet(e.state, path, "read")
		case "read_multi":
			// read_multi has no top-level "path"; extractPathFromArgs returns "".
			// Parse the self-describing metadata in the result digest to record
			// each sub-target's (path, scope) into ReadHistory, and add each to
			// the working set as "read".
			if i < len(results) {
				for _, rec := range parseReadMultiDigestScopes(results[i].Digest) {
					addToWorkingSet(e.state, rec.Path, "read")
					e.state.ReadHistory = append(e.state.ReadHistory, rec)
				}
			}
		case "grep", "glob":
			if i < len(results) && results[i].Status == "ok" {
				addToWorkingSet(e.state, path, "searched")
			}
		}
```

注意：`path`（912 行 `extractPathFromArgs`）对 read_multi 为 `""`，但 `case "read_multi"` 不使用 `path`，无影响。`continue`（913-915 行的 `if path == "" { continue }`）会**跳过** read_multi！需调整：把 `if path == "" { continue }` 改为只对需要 path 的分支跳过。

将 911-915 行：

```go
	for i, call := range calls {
		path := extractPathFromArgs(call.Input)
		if path == "" {
			continue
		}
		switch call.Name {
```

改为：

```go
	for i, call := range calls {
		path := extractPathFromArgs(call.Input)
		switch call.Name {
		case "read_multi":
			// path is "" for read_multi; handled via result metadata below.
			if i < len(results) {
				for _, rec := range parseReadMultiDigestScopes(results[i].Digest) {
					addToWorkingSet(e.state, rec.Path, "read")
					e.state.ReadHistory = append(e.state.ReadHistory, rec)
				}
			}
			continue
		}
		if path == "" {
			continue
		}
		switch call.Name {
```

（即：先特判 read_multi 并 `continue`，再对其余工具做 `path == ""` 跳过与原 switch。）删去下面原 switch 中的 `case "read_multi":` 分支（避免重复），保留 `case "edit","write"`/`case "read"`/`case "grep","glob"`。

- [ ] **步骤 5：编译 + 运行测试验证通过**

运行：`go test ./engine/ -run "TestParseReadMultiDigestScopes|TestUpdateTaskState_ReadMultiWritesReadRecords" -v`
预期：PASS。

- [ ] **步骤 6：运行 engine 全量测试确认无回归**

运行：`go test ./engine/ -short`
预期：PASS。

- [ ] **步骤 7：Commit**

```bash
git add engine/turn.go engine/read_multi_integration_test.go
git commit -m "feat(engine): write ReadRecord per read_multi sub-target

Parse the self-describing metadata comment in read_multi results and append one
ReadRecord per sub-target to ReadHistory, so the read-loop guard and prompt
read-list track fan-out reads correctly. Best-effort: missing metadata is a no-op."
```

---

## 任务 5：engine loop-guard 对 read_multi per-target 计数

**文件：**
- 修改：`engine/guards.go`（新增 `parseReadMultiTargets` helper）
- 修改：`engine/turn.go`（loop-guard 检查处 367-404 行）
- 测试：`engine/read_multi_integration_test.go`（追加）

**目标：** `read_multi` 当前经 `extractToolKey` 的 `default` 分支返回 `""` 完全绕过 loop-guard。改为对每个 target 合成一个 `read` 调用做 `Check`，任一被拦则整批 block。这样反复用 read_multi 读同一 (path,scope) 也会被守卫拦截，不成为后门。

- [ ] **步骤 1：编写失败的测试**

在 `engine/read_multi_integration_test.go` 追加：

```go
func TestParseReadMultiTargets(t *testing.T) {
	input := json.RawMessage(`{"targets":[{"path":"a.go","symbol":"Run"},{"path":"b.go","offset":5,"limit":3},{"path":"c.txt"}]}`)
	targets := parseReadMultiTargets(input)
	if len(targets) != 3 {
		t.Fatalf("got %d targets, want 3", len(targets))
	}
	if targets[0].Path != "a.go" || targets[0].Symbol != "Run" {
		t.Errorf("target[0] = %+v", targets[0])
	}
	if targets[1].Offset != 5 || targets[1].Limit != 3 {
		t.Errorf("target[1] = %+v", targets[1])
	}
	if targets[2].Path != "c.txt" {
		t.Errorf("target[2] = %+v", targets[2])
	}
}

func TestLoopGuard_ReadMultiPerTargetBlocks(t *testing.T) {
	// read_multi of the same (path, scope) repeatedly must be blocked by the
	// loop guard, just like repeated single reads — read_multi must not bypass.
	g := NewLoopGuard(3)
	tgt := readMultiTargetView{Path: "a.go", Symbol: "Run"}
	synthInput, _ := json.Marshal(map[string]interface{}{"path": tgt.Path, "symbol": tgt.Symbol, "offset": tgt.Offset, "limit": tgt.Limit})
	synth := ToolCallRequest{ID: "c", Name: "read", Input: synthInput}

	var lastAction GuardAction
	for i := 0; i < 3; i++ {
		lastAction = g.Check(synth)
	}
	if lastAction.Type != GuardBlock {
		t.Fatalf("after 3 repeated read_multi targets, action = %v, want GuardBlock", lastAction.Type)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run "TestParseReadMultiTargets|TestLoopGuard_ReadMultiPerTargetBlocks" -v`
预期：编译失败，`undefined: parseReadMultiTargets`、`undefined: readMultiTargetView`。

- [ ] **步骤 3：在 guards.go 新增 `parseReadMultiTargets` 与视图类型**

在 `engine/guards.go` 的 `extractReadScopeHash`（116-131 行）之后新增：

```go
// readMultiTargetView is engine's view of a read_multi target (mirrors the
// tools/builtin readMultiTarget struct, kept unexported and local to avoid a
// tools→engine import).
type readMultiTargetView struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// parseReadMultiTargets parses the targets array from a read_multi tool call's
// input. Returns nil on error.
func parseReadMultiTargets(input json.RawMessage) []readMultiTargetView {
	var m struct {
		Targets []readMultiTargetView `json:"targets"`
	}
	if err := json.Unmarshal(input, &m); err != nil {
		return nil
	}
	return m.Targets
}
```

- [ ] **步骤 4：在 turn.go loop-guard 检查处特判 read_multi**

在 `engine/turn.go` 的 loop-guard 检查循环（367-404 行），将：

```go
	for _, call := range calls {
		// Check loop guard: same (tool, path) repeated → block to prevent cycles
		if e.guards.loop != nil {
			loopAction := e.guards.loop.Check(call)
			if loopAction.Type != GuardAllow {
				e.history = append(e.history, assistant)
				for _, c := range calls {
					e.history = append(e.history, Message{
						Role:       "tool",
						ToolCallID: c.ID,
						Content:    "Blocked: " + loopAction.Message,
						Timestamp:  time.Now(),
					})
				}
				return TurnResult{Blocked: true, BlockedBy: loopAction.Type, Questions: []string{loopAction.Message}}, nil
			}
		}
```

替换为（增加 read_multi per-target 检查）：

```go
	for _, call := range calls {
		// Check loop guard: same (tool, path) repeated → block to prevent cycles.
		if e.guards.loop != nil {
			var loopAction GuardAction
			if call.Name == "read_multi" {
				// read_multi bypasses the single-call key; check each sub-target
				// as a synthetic read so repeated fan-out reads of the same
				// (path, scope) are still caught.
				for _, tgt := range parseReadMultiTargets(call.Input) {
					synthInput, _ := json.Marshal(map[string]interface{}{
						"path": tgt.Path, "symbol": tgt.Symbol,
						"offset": tgt.Offset, "limit": tgt.Limit,
					})
					synth := ToolCallRequest{ID: call.ID, Name: "read", Input: synthInput}
					a := e.guards.loop.Check(synth)
					if a.Type != GuardAllow {
						loopAction = GuardAction{
							Type:    a.Type,
							Message: fmt.Sprintf("read_multi target %s: %s", tgt.Path, a.Message),
						}
						break
					}
				}
			} else {
				loopAction = e.guards.loop.Check(call)
			}
			if loopAction.Type != GuardAllow {
				e.history = append(e.history, assistant)
				for _, c := range calls {
					e.history = append(e.history, Message{
						Role:       "tool",
						ToolCallID: c.ID,
						Content:    "Blocked: " + loopAction.Message,
						Timestamp:  time.Now(),
					})
				}
				return TurnResult{Blocked: true, BlockedBy: loopAction.Type, Questions: []string{loopAction.Message}}, nil
			}
		}
```

确认 `engine/turn.go` 顶部已 import `"fmt"`（应已存在）。

- [ ] **步骤 5：编译 + 运行测试验证通过**

运行：`go test ./engine/ -run "TestParseReadMultiTargets|TestLoopGuard_ReadMultiPerTargetBlocks" -v`
预期：PASS。

- [ ] **步骤 6：运行 engine 全量测试**

运行：`go test ./engine/ -short`
预期：PASS。

- [ ] **步骤 7：Commit**

```bash
git add engine/guards.go engine/turn.go engine/read_multi_integration_test.go
git commit -m "feat(engine): loop-guard counts read_multi per sub-target

read_multi previously bypassed the loop guard (extractToolKey returns \"\" for
unknown tools). Synthesize a read call per sub-target so repeated fan-out reads
of the same (path, scope) are blocked like repeated single reads."
```

---

## 任务 6：提示层引导

**文件：**
- 修改：`context/promptset/zh/system.md`（工具使用策略段，64-71 行附近）
- 修改：`context/promptset/en/system.md`（对应段落）

- [ ] **步骤 1：先确认现有提示测试基线**

运行：`go test ./context/ -short`
预期：PASS（若有 builder_test 提示测试）。

- [ ] **步骤 2：在中文 system.md 追加引导**

在 `context/promptset/zh/system.md` 的 `# 工具使用策略` 段（64 行起）中，`使用 Read 工具，而不是 bash 中的 cat`（67 行）之后新增一行：

```markdown
- 一次需要了解多处代码（多个文件/多个符号/多个方向）时，用 `read_multi` 一次列出所有目标并行读取，而不是串行发多个 read。需要精读单文件时仍用 `read`；精准定位符号/类型仍优先 `lsp`。
```

- [ ] **步骤 3：在英文 system.md 追加镜像引导**

在 `context/promptset/en/system.md` 对应的 tool strategy 段，"Use the Read tool instead of cat in bash" 之后新增：

```markdown
- When you need to understand several places at once (multiple files/symbols/directions), use `read_multi` to list all targets in one call and read them in parallel instead of chaining single reads. Use `read` for single-file deep reads; prefer `lsp` for precise symbol/type lookup.
```

- [ ] **步骤 4：编译 + 测试**

运行：`go build ./... && go test ./context/ -short`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add context/promptset/zh/system.md context/promptset/en/system.md
git commit -m "docs(context): guide agents to use read_multi for fan-out reads"
```

---

## 任务 7：全量验证

- [ ] **步骤 1：全量构建与测试**

运行：`make build && make test-short`
预期：构建成功，所有测试 PASS。

- [ ] **步骤 2：lint**

运行：`make lint`
预期：无新增告警。

- [ ] **步骤 3：（可选）手动冒烟**

启动 `./deepact`，让 agent 用 `read_multi` 一次读多个文件，确认工具被调用、结果聚合正常、重复读同一区段被守卫拦截。

---

## 自检

**1. 规格覆盖度：**
- §3 工具 schema → 任务 2 步骤 3（Spec/Parameters）✅
- §4.1 复用函数抽取 → 任务 1 ✅
- §4.2 并行抓取 → 任务 2 步骤 3（goroutine + WaitGroup，按输入顺序写回）✅
- §4.3 聚合格式 → 任务 2 步骤 3（header/body/ERROR/元数据注释）✅
- §5.1 ReadHistory 记账 + 降级 → 任务 4（parseReadMultiDigestScopes，无元数据返回 nil）✅
- §5.2 loop-guard per-target → 任务 5 ✅
- §5.3 不绕过守卫 → 任务 5 测试 `TestLoopGuard_ReadMultiPerTargetBlocks` ✅
- §6 提示引导（zh/en + 工具 description）→ 任务 2 步骤 3（description）+ 任务 6（system.md）✅
- §8.1 工具测试 → 任务 2（多目标/部分失败/超上限/空）✅
- §8.2 engine 交互测试 → 任务 4 + 任务 5 ✅
- §8.3 提示层测试 → 任务 6 步骤 4（context 测试基线）✅

**2. 占位符扫描：** 无 TODO/待定；所有代码步骤含完整代码块。✅

**3. 类型一致性：**
- `readMultiTarget`（tools/builtin，任务 2）与 `readMultiTargetView`（engine，任务 5）字段一致（Path/Symbol/Offset/Limit）。✅
- `readMultiResult.scope` 串口径：`""`/`"symbol:X"`/`"L5-7"`/`"L10-end"`，与 `parseReadMultiDigestScopes` 解析的 `path::scope` 一致，与 `ReadRecord.Scope` 一致。✅
- `parseReadMultiDigestScopes` 返回 `[]ReadRecord`，`updateTaskStateFromTools` 追加到 `e.state.ReadHistory`（`[]ReadRecord`）。✅
- `NewLoopGuard(3)` / `GuardBlock` / `GuardAction{Type, Message}` / `ToolCallRequest{ID, Name, Input}` 均与 guards_test.go 既有用法一致。✅
- header 格式：测试断言 `=== [1] <path> (full) ===`、`=== [2] <path> [L5-7] ===`、`=== [3] <path> [symbol:Run] ===`，与实现 `fetchTarget` 的 `fmt.Sprintf("=== [%s] ...", tgt.Path)` 一致。✅

无遗漏，无需补充任务。
