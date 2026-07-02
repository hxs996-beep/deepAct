# Diff 纯文本展示修复设计

**日期**: 2026-06-30
**范围**: `ui/model.go`（diff 渲染）、新增 `ui/diff_render_test.go`、扩展 `ui/selection_test.go`
**目标**: 修复 UI 中代码 diff 区域鼠标长按选错行、上下滚动导致下一行信息渲染到上一行、滚动残留花屏三个症状。

## 背景

当前 diff 展示是一条样式化渲染链：`parseDiffHunks` 拆 hunk → `renderDiffHunkBlock` 给每行加 ANSI 前景色（`+`绿/`-`红/`@@`黄/行号灰）+ 行号前缀 + 全宽 pad → 塞进消息流。鼠标选择是一套自研快照系统：mouse-down 快照 rendered+plain 行数组，`screenToLine` 把屏幕坐标映射到行号，再用 `\x1b[7m` 反白高亮。代码里已堆积多帧 diff 补丁注释（frame diff mis-repaints、scroll-ghosting、diff-row shift bug），一直在和终端渲染机制对抗。

用户诉求：把代码 diff 改为纯文本展示（保留极简单层前景色），根除上述 bug。约束：必须保留鼠标长按拖选 + 滚轮滚动 + 复制（长文本刚需），因此 mouse capture 不能关、终端原生选区不可用、自研选区系统必须保留。

## 根因诊断

bug 不在"自研选区"本身，而是 diff 渲染层 4 个缺陷叠加。选区列计算本身已按显示宽度对齐（`selPoint.Col` + `lipgloss.Width` 逐 rune），不是根因。

| # | 缺陷 | 位置 | 症状 |
|---|------|------|------|
| R1 | 空行被 `continue` 丢弃 | `renderDiffHunkBlock` model.go:2006 | 屏幕行 ≠ 数据行 → 选错行、滚动错位（a/b） |
| R2 | 未剥离 `\r` | `renderDiffHunkBlock`/`renderDiffHunkFlat`（对比 `parseOutputLines`:1754 有 `TrimRight(lines[i], "\r")`） | 终端回车覆盖 → 下行渲染到上行（b） |
| R3 | pad 宽度不一致 | `renderDiffBlock` padLine 用 `m.width`，View 截断用 `contentWidth`，renderBody 用 `scrollContentWidth` | 滚动残留/花屏（e） |
| R4 | 长行无硬截断提示 | 当前靠 View 末尾 `ansi.Truncate(...,"")` 静默截 | 长行截断后无提示（d 潜伏） |

## 修复方案

### 决策摘要

| 议题 | 决策 |
|------|------|
| 渲染风格 | 方案 B：纯文本行 + 单层前景色（`+`绿/`-`红/`@@`黄/行号灰） |
| 选区机制 | 保留自研快照 + 反白高亮（mouse capture 不能关） |
| 反白高亮 | 保留（修根因后不再触发 bug） |
| 长行处理 | 硬截断 + 行尾 `…` |
| 宽字符 | 显示宽度全程统一（`lipgloss.Width` 逐 rune） |
| 改动范围 | A：只动 diff 渲染 + 选区列对齐，其余 UI 不碰 |
| 帧差异补丁 | 本次不删，修完实测再评估 |

### R1 + R2：空行与回车（`renderDiffHunkBlock` / `renderDiffHunkFlat`）

- 每行先 `strings.TrimRight(raw, "\r")` 剥离回车（R2）。
- 空行不再静默 `continue`：在 `continue` 前先 append 一个对齐的占位空行（block 版 `"    "+diffContextStyle.Render("")`，flat 版 `"    \n"`），保持前缀缩进一致（R1）。
- 这样行数组长度 == 屏幕行数，`screenToLine` 映射天然准确。
- 其余 `@@` / `+` / `-` / context 逻辑不变。

### R4：长行硬截断 + 行尾提示

- 新增 `truncateVisual(s string, maxW int) string` 辅助函数：按显示宽度逐 rune 截断（`lipgloss.Width`），保留 ANSI 样式边界（不切断转义序列），不自动加尾标记（由调用方决定加 `…`）。
- `renderDiffHunkBlock` 已有 `maxWidth` 参数；每行拼好后按 `maxWidth` 截断，超出则 `truncateVisual(rendered, maxWidth-1) + "…"`，断言 `lipgloss.Width(stripAnsi(rendered)) <= maxWidth`。
- `renderDiffHunkFlat` 用于 `renderToolSummary` 的消息文本流，宽度语义不同于 block（非定宽 viewport）。本次对 flat 版**只修 R1/R2，不加硬截断**——summary 文本流跟随终端软折行，硬截断反而破坏阅读。flat 版签名不变。

### R3：pad 宽度统一

- 移除 `renderDiffBlock` 的 `padLine` 及其全宽填充逻辑。
- 理由：View Step 7 已对全 body 统一 `ansi.Truncate(lines[i], contentWidth, "")`，diff 行单独 pad 是多余且引入 `m.width`/`contentWidth`/`scrollContentWidth` 三宽度不一致的根源。移除后所有行宽度由 View 统一管理，R3 根除。
- 原 pad 为"显式写满每格防 `\x1b[K` 残留"，但 Truncate + 终端默认清行已足够。

## 选区层与 View 对齐

- `screenToLine`：不动。R1 修掉后屏幕行 = 数据行，映射自动准确。
- `applySelectionHighlight` / `reverseHighlightLine`：不动。列计算已是显示宽度对齐，R2 修掉 `\r` 后无被误处理的控制字符。
- 快照 freeze 机制（model.go:734-761）：保留，与本次根因无关。
- 帧差异全重绘补丁（model.go:254-259、333-336 的 `repaintCmd()`）：本次不删，修完实测后再评估是否精简，避免引入回归。
- View 截断：移除 padLine 后，diff 行与其他行走同一条 `ansi.Truncate(..., contentWidth, "")` 路径，宽度天然一致。

## 测试方案

### 渲染层单元测试（新增 `ui/diff_render_test.go`，表驱动）

| 测试 | 验证 | 断言 |
|------|------|------|
| `TestRenderDiffHunkBlock_PreservesEmptyLines` | R1 | 含空 context 行的 hunk，输出行数 == 输入行数（含 `@@` 行），空行对应一个占位行 |
| `TestRenderDiffHunkBlock_StripsCR` | R2 | 含 `\r\n` 的 hunk，输出每行不含 `\r` |
| `TestRenderDiffHunkBlock_HardTruncatesLongLine` | R4 | 超长代码行截到 `maxWidth`，末尾有 `…`，显示宽度 ≤ maxWidth |
| `TestRenderDiffHunkBlock_LineNumbersAligned` | 基线 | `+`/`-`/context 行号正确递增、对齐 |
| `TestRenderDiffHunkBlock_WideCharWidth` | c | 含中文行，`lipgloss.Width(stripAnsi(line)) <= maxWidth` |
| `TestRenderDiffHunkFlat_PreservesEmptyLines` | R1 | flat 版空行保留 |
| `TestRenderDiffHunkFlat_StripsCR` | R2 | flat 版剥 `\r` |

### 截断辅助函数测试

`TestTruncateVisual`：普通 / 宽字符 / 含 ANSI 序列三种输入，断言显示宽度 = 预期、ANSI 序列未被切断。

### 选区映射回归测试（扩展 `ui/selection_test.go`）

| 测试 | 验证 | 断言 |
|------|------|------|
| `TestScreenToLine_AfterEmptyLineFix` | R1 修复后 | 构造含空行的 rendered/plain 数组，screenToLine 屏幕→数据行 1:1 准确 |
| `TestExtractSelectionText_NoCRInOutput` | R2 | 选中含原 `\r` 的 diff 行，复制结果不含 `\r` |

### 验收

- `make test` 全绿（race detector）
- `make lint` 通过
- 手动验收：跑一个产生含空行/中文/长行 diff 的 edit，验证选错行（a）、滚动错位（b）、滚动残留（e）三症状消失，拖选复制内容正确。

## 不做（YAGNI）

- 不动 search/exec block 渲染（未报问题）。
- 不删帧差异全重绘补丁（修完实测再评估）。
- flat 版不加硬截断（消息文本流语义不同）。
- 不改 `screenToLine` / `applySelectionHighlight` / `reverseHighlightLine` / freeze 机制。
- 不引入软折行、横向滚动。
