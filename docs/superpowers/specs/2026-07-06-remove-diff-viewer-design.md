# 移除全屏 diff viewer 交互

## 背景

当前文件修改后的 diff 展示流程：每个 hunk 渲染为折叠的 summary 行（`[N] @@ -1,3 +1,3 @@    +2  -1    点击展开`），点击该行跳转全屏 diff viewer 显示完整 hunk 内容，支持滚轮滚动、拖拽选择、复制，ESC 退出。

用户要求移除"点击展开"文字和点击跳转全屏 viewer 的功能，保留折叠的统计摘要行。

## 目标

- 保留折叠的 hunk summary 行（统计 `+N -N`）
- 移除"点击展开"文字
- 移除点击 hunk summary 行跳转全屏 diff viewer 的交互
- 清理所有因此产生的死代码

## 改动详情

### 1. `ui/diff_viewer.go` — 大幅精简

**保留（修改）：**
- `countHunkAddsDeletes()` — 不变
- `hunkSummaryLine()` — 移除 `hintStyle` 变量和 `"点击展开"` 文字，返回 `label + hunkHeader + "    " + changes`

**删除（仅服务于全屏 viewer）：**
- `hunkHit` 类型（L11-17）
- `renderDiffViewer()` 函数（L53-85）
- `hunkSummaryRe` 正则（L87-88）
- `hitTestHunk()` 函数（L90-125）
- `hunkLineEntry` 类型（L127-132）
- `buildHunkLineMap()` 函数（L134-194）

删除后若 `regexp` import 无其他使用则移除。

### 2. `ui/model.go` — 移除 viewer 状态和交互

**删除字段（L148-150）：**
- `diffViewerActive`
- `diffViewerHunk`
- 注释 `// Diff hunk collapse viewer`

**删除交互逻辑：**
- 鼠标处理中 `if m.diffViewerActive { ... }` 整块（L243-333）：包含滚轮滚动、拖拽选择、点击 press/release
- 点击 release 中打开 viewer 的分支（L425-437）：保留 `m.selection = SelectionState{}` 清除选择
- ESC 处理中退出 viewer 的分支（L1136-1142）

**删除渲染逻辑：**
- `if m.diffViewerActive { ... }` 渲染分支（L887-932），将 `} else if frozen {` 改为 `if frozen {`
- `computeDiffViewerLayout()` 函数（L804-817）

**级联死代码清理（renderDiffViewer 移除后失去唯一生产调用者）：**
- `renderDiffHunkBlock()` 函数（L2202-2240）
- `diffDeleteStyle`、`diffInsertStyle`、`diffHunkHeaderStyle`（L2187-2189）
- `diffStylesOnce`（L2190）
- `initDiffStyles()` 函数（L2193-2200）
- `// diff styles cached for performance` 注释（L2185）

**更新注释：**
- L2126、L2162 中 `// global 1-based hunk number across all files (matches hitTestHunk)` → 移除 `(matches hitTestHunk)` 引用

删除后检查 `sync` import 是否仍需要（`diffStylesOnce` 是唯一 `sync.Once` 使用者则移除）。

### 3. `ui/diff_render_test.go` — 移除相关测试

**删除：**
- `TestRenderDiffViewer_RendersHunkFullscreen`（L198-219）
- `TestHitTestHunk`（L221-267）
- `TestESCExitsDiffViewer`（L269-286）
- `TestRenderDiffViewer_FromMessageSnapshot`（L304-323）
- `TestRenderDiffHunkBlock_PreservesEmptyLines`（L17-25）
- `TestRenderDiffHunkBlock_StripsCR`（L27-36）
- `TestRenderDiffHunkBlock_LineNumbersAligned`（L38-59）
- `TestRenderDiffHunkBlock_PreservesRawContent`（L61-73）
- `TestRenderDiffHunkBlock_WideCharPreserved`（L75-87）

**保留：**
- `TestRenderDiffBlock_NoPadToTerminalWidth`（L89-106）
- `TestCountHunkAddsDeletes`（L108-148）
- `TestRenderDiffBlock_CollapsesHunks`（L150-196）
- `TestRenderToolSummary_CollapsesHunks`（L288-302）
- `TestFinishStreaming_SnapshotsToolTree`（L325-359）
- `buildHunk()` 辅助函数（L11-15）— 检查是否仍被保留的测试使用，若否则删除

删除后检查 `tea` import 是否仍需要（`TestESCExitsDiffViewer` 是唯一 `tea` 使用者则移除）。

## 不改动

- `renderDiffBlock()` — 渲染 Changes 块和 hunk summary 行，不涉及 viewer
- `renderToolSummary()` — 渲染 toolsummary 消息中的 hunk summary
- `parseDiffHunks()`、`isDiffContent()`、`splitDiff()` — 解析 diff 内容，与 viewer 无关
- 正常的鼠标选择/复制/滚轮交互 — 不受影响

## 验证

1. `go build ./ui/` 编译通过
2. `go test ./ui/` 测试通过
3. `go vet ./ui/` 无警告
