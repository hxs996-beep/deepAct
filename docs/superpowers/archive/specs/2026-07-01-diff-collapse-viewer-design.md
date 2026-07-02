# Diff 折叠展示设计

**日期**: 2026-07-01
**目标**: 把 diff 从"在滚动 body 内与其他行混排展示"改为"hunk 级折叠 + 点击全屏展开"，从架构上绕开 diff 混排滚动时的 ghosting（其他行字符渗入 diff 行、滚动后污染行扩大）。

## 背景

经过多轮 systematic-debugging 排查，确认 ghosting 的触发条件是 **diff 行与其他行混排在滚动 body 内一起滚动**。已排除的根因：Bubble Tea canSkip 帧差分（nonce 探针让每行每帧字节不同后 ghosting 仍在）、`\x1b[K` 缺失、`\t` 制表符、`\r` 回车、ANSI 光标错位、padLine 宽度。复制（走自研选区 plain 快照）正确只证明 View 数据层正确，不证明屏幕显示正确。

关键证据：**ghosting 只在 diff 出现，且只在混排滚动时出现**。全屏 diff 内全是 diff 行（无其他行可渗入），触发条件被根除。因此采用折叠方案从架构上绕开，而非继续在渲染层修补。

## 方案

### 状态机

Model 新增字段：
- `diffViewerActive bool` — 是否处于全屏 diff 查看模式
- `diffViewerHunk` — 指向当前展开的 hunk（用 toolTree 索引定位：edit 节点索引 + hunk 子索引）

点击 hunk 行进入 `diffViewerActive=true`，ESC 退出回 `false`。

### 折叠视图（默认）

`renderDiffBlock` 改为 hunk 级折叠。每个已完成的 edit/write 节点下，每个 hunk 显示一行摘要：

```
▍ [~] Changes
  :: foo.go ✓
    [1] @@ -1,4 +1,4 @@        +2  -1
    [2] @@ -20,3 +20,5 @@      +3  -0
```

每个 hunk 摘要行：序号 `[N]` + hunk 头（`@@ ... @@`）+ `+N -M`（新增/删除行数）。不显示 hunk 内容。

行号 `[N]` 用于鼠标点击命中映射（屏幕行 → hunk 索引）。

### 全屏 diff viewer（展开）

点击 hunk 行 → 进入全屏：
- 该 hunk 完整内容占满 body 区域（复用 `renderDiffHunkBlock` 渲染，宽度为 `scrollContentWidth`）
- body 内滚轮滚动（复用 `scrollOffset`，作用于 hunk 内容行）
- **全屏内禁用拖选**（避免 ghosting 复现 + 简化交互）
- ESC 退出回折叠视图

View 的 body 渲染分支新增：`if m.diffViewerActive { 渲染 hunk 全屏内容 } else { 现有 renderBody }`。

### 鼠标交互

**折叠视图**：
- 点击 hunk 行（mouse-down + release 同位置，复用现有"单点击"判断 + 命中 hunk 行）→ 展开该 hunk
- 滚轮滚动 body（现有）
- 拖选仍可用（折叠摘要行是普通文本，无 ghosting）

**全屏 diff**：
- 滚轮滚动 hunk 内容
- ESC 退出
- 禁拖选

### 点击 vs 拖选区分

复用现有 mouse 处理（model.go:278-337）：mouse-down 记录起点，motion 更新选区（拖动），release 时若 `Start == End`（无 motion）= 单点击。

在现有 release 逻辑里增加判断：单点击且点击位置命中某个 hunk 摘要行 → 展开。拖选（有 motion）仍正常工作，不展开。点击空白处仍是清选区。

命中映射：折叠视图的 hunk 摘要行在 body 中的位置需可由屏幕坐标反推。与现有 `screenToLine` 类似，但映射到"hunk 索引"而非"数据行"。

### 保留

- R1/R2/R4 修复保留（空行占位、剥 `\r`、长行截断）— 全屏 diff 渲染复用 `renderDiffHunkBlock`
- R3（padLine 移除）保留
- 选区系统、mouse capture 保留
- `repaintCmd`（WindowSizeMsg 版本）保留 — 不闪屏

### 不做（YAGNI）

- 不深究 ghosting 渲染层根因（折叠方案绕开，无需修补）
- 全屏 diff 不支持拖选复制（需复制时退回折叠视图，或后续再处理）
- 不改 search/exec block 渲染
- 不改 Bubble Tea 版本/渲染器

## 实现细节

- **`+N -M` 行数计算**：遍历 hunk 的 `DetailFull` 行，统计首字符为 `+`（非 `+++`）的行数和 `-`（非 `---`）的行数。
- **命中映射**：折叠视图渲染时，记录每个 hunk 摘要行在 body 行数组中的索引（`[]hunkHit{nodeIdx, childIdx, lineIdx}`）。鼠标 release 时用 `screenToLine` 得到点击的 body 行索引，查表命中 hunk。
- **全屏滚动边界**：`maxScroll = max(0, hunkDisplayLines - bodyHeight)`，复用现有 scrollOffset 钳制逻辑。
- **`diffViewerHunk` 数据结构**：`struct{ nodeIdx, childIdx int }`，索引 toolTree 中的 edit 节点及其 Children 中的 hunk。不用指针（toolTree 可变，索引更稳）。
- **ESC 退出**：在 `handleKey` 里，`diffViewerActive` 为 true 时拦截 ESC，置 `diffViewerActive=false` 并重置 scrollOffset，不传给输入框。
- **全屏禁拖选**：`diffViewerActive` 为 true 时，mouse-down/release 不进入选区逻辑（直接 return 或仅处理滚轮）。
