# ANSI 感知自动换行设计

日期：2026-07-04
状态：设计阶段

## 问题

DeepAct CLI 展示 LLM 输出时存在两个问题：

1. **文字太长没有自动换行**：含 ANSI 颜色码的长行无法换行，需要拉伸窗体才能看到后面的文字
2. **超出部分静默截断**：`View()` Step 7 用 `ansi.Truncate` 硬截断超宽行，无任何提示

这两个问题在主 agent 和子 agent 的输出中均存在。

## 根因

渲染管线中有两个换行断层：

### 断层 1：`wrapLines` 跳过含 ANSI 的行

`ui/model.go:3025-3038`：

```go
func wrapLines(lines []string, width int) []string {
    for _, line := range lines {
        if lipgloss.Width(line) <= width {
            result = append(result, line)
            continue
        }
        // 含 ANSI 的行直接跳过，不换行
        if strings.Contains(line, "\033[") {
            result = append(result, line)  // ← 超宽行透传
        } else {
            result = append(result, wrapText(line, width)...)
        }
    }
}
```

所有经过 `wrapLines` 的带颜色行（spinner、sub-agent 面板等）都会被透传。

### 断层 2：Step 7 硬截断

`ui/model.go:1008-1016`：

```go
// Step 7: Truncate all body lines to terminal width, then pad
for i := range lines {
    lines[i] = ansi.Truncate(lines[i], contentWidth, "")
    if w := ansi.StringWidth(lines[i]); w < contentWidth {
        lines[i] += strings.Repeat(" ", contentWidth-w)
    }
}
```

上游透传的超宽行在这里被无声截断。

## 设计

### 核心思路

新增 **ANSI 感知的换行函数 `wrapLineAnsi`**，修复所有渲染路径中的换行逻辑，确保超宽行在到达 Step 7 之前已被正确换行。Step 7 保持不变作为安全网。

```
                渲染阶段（本次修改）            安全网（不动）
                ┌─────────────────────┐      ┌──────────┐
renderMessage ──┤                     │      │          │
renderStreaming─┤  wrapLineAnsi       ├──────┤  Step 7   │──→ 终端
renderSpinners──┤  (ANSI 感知换行)     │      │ Truncate  │
renderSubAgents─┤                     │      │          │
renderToolTree ─┤                     │      └──────────┘
                └─────────────────────┘
```

### 新增函数：`wrapLineAnsi`

位置：`ui/model.go`，紧邻 `wrapLine`（L2981 之后）

```go
// wrapLineAnsi wraps a line that may contain ANSI escape sequences to fit
// within the given visual width. It preserves all escape sequences intact,
// re-emits active SGR (Select Graphic Rendition) sequences at the start of
// continuation lines, and emits SGR reset at the end of each wrapped line
// to prevent color bleeding.
//
// Word-wrap: prefers breaking at spaces. Falls back to hard-break at width
// boundary when no space is found within the line.
func wrapLineAnsi(line string, width int) []string
```

#### 算法

```
输入：line（含 ANSI 码的字符串），width（目标视觉宽度）

1. 如果 lipgloss.Width(line) <= width → 返回 [line]
2. 逐字符扫描，跟踪：
   - visualCol：当前视觉列位置
   - activeSGR：活跃的 SGR 序列列表（如 \x1b[31m）
   - lastSpaceIdx：当前段内最后一个空格在原始字符串中的字节索引
   - inEscape：是否在 ANSI 转义序列内部
3. 到达宽度边界（visualCol >= width）时：
   a. 如果有 lastSpaceIdx → 在空格处断行（word-wrap）
   b. 否则 → 在当前位置硬断
4. 断行后：
   a. 当前行末尾追加 \x1b[0m（防止颜色泄漏）
   b. 下一行开头重放 activeSGR（保持颜色延续）
5. 继续扫描剩余内容
6. 返回 []string
```

#### 边界处理

- **ANSI 序列完整性**：绝不在 ANSI 序列中间断行（通过 `inEscape` 状态机保护）
- **宽字符**：CJK 字符和 emoji 使用 `lipgloss.Width` 计算视觉宽度（占 2 列）
- **SGR 重放**：只重放 SGR 序列（`\x1b[...m`），不重放光标移动等其他序列
- **空行**：返回 `[""]`
- **width <= 0**：返回 `[line]`

### 修改点

| 位置 | 文件:行 | 当前行为 | 改为 |
|------|--------|---------|------|
| `wrapLine` | model.go:2981 | 不处理 ANSI，按 rune 切分 | 检测到 ANSI → 委托 `wrapLineAnsi` |
| `wrapLines` | model.go:3035-3038 | 含 ANSI 跳过不换行 | 调用 `wrapLineAnsi`（或通过 `wrapLine`） |
| `renderSubAgentPanel` | model.go:2369 | `ExecBlockStyle.Width(width).Render()` 可能截断 | 渲染后对超宽行调用 `wrapLineAnsi` |
| `renderMemberProgress` | model.go:2423 | 同上 | 同上 |
| `renderTDDStatus` | model.go:2460+ | 同上 | 同上 |

### 不改动的部分

- **Step 7**（`model.go:1008-1016`）：保持 `ansi.Truncate` 作为安全网。上游正确换行后，正常情况下不再触发
- **`renderMarkdown`**：glamour 已有 `WithWordWrap(width-2)`，工作正常
- **`renderStreaming`**：通过 `wrapText` → `wrapLine` 路径，修复 `wrapLine` 后自动修复
- **`renderMessage` 的 user/system 分支**：通过 `wrapText` → `wrapLine` 路径，同上

## 测试策略

### 单元测试（`ui/model_test.go`）

1. **纯 ASCII 超宽行**：`"hello world hello world"` @ width=12 → `["hello world", "hello world"]`
2. **含 ANSI SGR 的超宽行**：`"\x1b[31mhello world hello world\x1b[0m"` @ width=12 → `["\x1b[31mhello world\x1b[0m", "\x1b[31mhello world\x1b[0m"]`
3. **ANSI 序列在断点中间**：确保不在 `\x1b[31m` 中间断开
4. **宽字符（中文）**：`"你好世界你好世界"` @ width=8 → `["你好世界", "你好世界"]`
5. **混合 ANSI + 宽字符**：`"\x1b[32m你好世界你好世界\x1b[0m"` @ width=8
6. **无需换行**：短行原样返回
7. **width=0**：返回原行
8. **空字符串**：返回 `[""]`
9. **多个 SGR 序列叠加**：`\x1b[1m\x1b[31m...` → 断行后两个都重放
10. **非 SGR 的 ANSI 序列**：光标移动等不应被重放

### 集成验证

- 启动 DeepAct CLI，发送一条会产生长输出（如长代码块）的请求
- 确认输出在窄终端窗口中自动换行，无需水平滚动
- 确认颜色在换行后正确延续
- 确认子 agent 面板中的超长 goal/summary 正确换行
