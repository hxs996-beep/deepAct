# Diff 完成后折叠 + 点击展开 设计

**日期**: 2026-07-01
**目标**: 修复工具执行**完成后** diff 输出的 ghosting（其他行字符渗入 diff 行、滚动后扩大）。完成后 diff 走 `renderToolSummary` → `renderDiffHunkFlat` 输出完整内容到 toolsummary 消息，混排滚动触发 ghosting。改为折叠摘要 + 点击展开全屏查看。

## 背景

DeepAct 有两条 diff 渲染路径：
- **streaming 中**：`renderDiffBlock`（toolTree）—— 已改为折叠摘要 + 点击展开
- **完成后**：`finishStreaming` 把完整 diff 渲染成 toolsummary 消息（`renderDiffHunkFlat`），塞进 messages，**然后清空 toolTree**

之前的折叠改动只改了 streaming 路径，完成后仍输出完整 diff → ghosting 复现。本设计补上完成后路径：折叠摘要 + 把 toolTree 快照存进消息，支持完成后点击展开。

ghosting 根因（diff 行与其他行混排滚动）不深究——折叠后完成后无 diff 行混排，从架构上绕开。

## 数据模型

### DisplayMessage 扩展

```go
type DisplayMessage struct {
	Role     string
	Content  string
	ToolTree []ToolNode // 仅 toolsummary 消息：完成时的 toolTree 快照，供点击展开
}
```

DisplayMessage 无 session 持久化（纯内存），扩展字段不影响序列化。

### finishStreaming 深拷贝快照

```go
if len(m.toolTree) > 0 {
	snapshot := append([]ToolNode(nil), m.toolTree...)
	for i := range snapshot {
		snapshot[i].Children = append([]ToolNode(nil), snapshot[i].Children...)
	}
	m.messages = append(m.messages, DisplayMessage{
		Role:     "toolsummary",
		Content:  renderToolSummary(m.toolTree), // 折叠摘要
		ToolTree: snapshot,
	})
	m.toolTree = nil
}
```

### hunkHit 扩展

```go
type hunkHit struct {
	msgIdx   int // -1 = streaming (m.toolTree), >=0 = m.messages[msgIdx].ToolTree
	nodeIdx  int
	childIdx int
}
```

## 折叠摘要渲染

### renderToolSummary 改折叠

每个 edit/write hunk 输出一行摘要（`hunkSummaryLine`），不输出 hunk 内容：

```
● 2 tools executed, 1 files modified
  ✎ foo.go
  [1] @@ -1,3 +1,3 @@    +2  -1
  [2] @@ -10,2 +10,2 @@  +1  -1
```

### 删除 renderDiffHunkFlat

`renderDiffHunkFlat` 唯一调用方是 `renderToolSummary`。改折叠后无调用方，删除（YAGNI）。其 R1/R2 修复（空行占位、剥 `\r`）不再需要——折叠摘要不输出 hunk 内容。`renderDiffHunkBlock`（全屏 viewer 用，含 R1/R2/R4）保留。

### streaming 路径

`renderDiffBlock` 已是折叠摘要（之前改动），保持不变。两路径视觉一致。

## 点击命中 + 统一入口

### hitTestHunk 改签名

```go
func hitTestHunk(bodyLineIdx int, bodyPlain []string, tree []ToolNode) (hunkHit, bool)
```

接收数据源 ToolTree，匹配 `[N]` 序号按 tree 的 edit/write hunk 顺序定位，返回 `hunkHit{nodeIdx, childIdx}`（msgIdx 由调用方填）。

### buildHunkLineMap（新增）

mouse release 单点击时调用，重建 body 行 → hunk 映射：

```go
func (m Model) buildHunkLineMap(width int) []hunkLineEntry
```

遍历 `sel.Plain`（mouse-down 快照的 plain 行数组），对每个 `[N]` 摘要行，按消息渲染顺序累加行号定位该行属于哪条 toolsummary 消息或 streaming 区域：
- streaming 区域（`m.toolTree` 非空且未进消息）→ `msgIdx = -1`，`hitTestHunk(line, plain, m.toolTree)`
- toolsummary 消息区域 → `msgIdx = 该消息索引`，`hitTestHunk(line, plain, msg.ToolTree)`

返回 `[]hunkLineEntry{msgIdx, lineIdx, hit}`。

行范围定位算法：逐条渲染 `m.messages` 累加行数（复用 renderMessage 的行数），toolTree 区域行数 = renderDiffBlock 输出行数。具体在实现计划阶段细化。

### mouse release 单点击

```go
if sel.Start == sel.End {
	hits := m.buildHunkLineMap(scrollContentWidth)
	for _, e := range hits {
		if e.lineIdx == sel.End.Line {
			hit := e.hit
			hit.msgIdx = e.msgIdx
			m.diffViewerActive = true
			m.diffViewerHunk = hit
			m.scrollOffset = 0
			m.selection = SelectionState{}
			return m, m.repaintCmd()
		}
	}
	m.selection = SelectionState{}
}
```

## 全屏 viewer

### renderDiffViewer 按数据源取

```go
func (m Model) renderDiffViewer(width int) []string {
	h := m.diffViewerHunk
	var tree []ToolNode
	if h.msgIdx < 0 {
		tree = m.toolTree
	} else if h.msgIdx < len(m.messages) {
		tree = m.messages[h.msgIdx].ToolTree
	}
	if len(tree) == 0 || h.nodeIdx >= len(tree) {
		return nil
	}
	node := tree[h.nodeIdx]
	if h.childIdx >= len(node.Children) {
		return nil
	}
	child := node.Children[h.childIdx]
	// 渲染（同现有：文件名 + hunk 头 + renderDiffHunkBlock）
}
```

### 已有改动保留

- View 的 `diffViewerActive` 分支 ✓
- handleKey ESC 退出 ✓
- 全屏 mouse 仅滚轮禁拖选 ✓
- streaming `renderDiffBlock` 折叠 ✓

## 测试方案

- `TestRenderToolSummary_CollapsesHunks`：完成后摘要不含 hunk 内容（无 old/new/ctx），含 `[N]` + `+A -D`
- `TestRenderDiffViewer_FromMessageSnapshot`：msgIdx>=0 时从消息 ToolTree 取 hunk 渲染全屏
- `TestRenderDiffViewer_FromLiveToolTree`：msgIdx=-1 时从 m.toolTree 取（streaming）
- `TestHitTestHunk_WithTree`：hitTestHunk 接收 tree 参数，按 [N] 定位
- `TestBuildHunkLineMap`：streaming + 多条 toolsummary 消息混合，按行号 + msgIdx 正确定位
- `TestFinishStreaming_SnapshotsToolTree`：完成后消息含 ToolTree 快照，且 m.toolTree 清空

## 不做（YAGNI）

- 不深究 ghosting 渲染层根因（折叠绕开）
- 不持久化 ToolTree 快照（无 session 存储）
- 不改 streaming 折叠逻辑（已存在）
- 不改 search/exec block 渲染

## 待细化（实现计划阶段）

- `buildHunkLineMap` 的行范围累加算法（消息行数计算、streaming 区域边界）
- `hitTestHunk` 改签名后现有调用点（streaming release）的适配
- `hunkHit.lineIdx` 字段是否仍需要（可能移除）
