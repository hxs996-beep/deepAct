# 分析报告闸门（Analysis Report Gate）设计

## 日期
2026-07-14

## 背景与问题

### 场景复现
用户输入："看下当前项目，看下哪些地方用关键字做判断让流程继续，需要找出来需要替换机制实现。"

Agent 行为：执行多轮搜索（grep/read/lsp）后，直接输出"现在实施所有改动。先改三个源文件，再更新测试。"并发出 edit/write 工具调用。

用户看到的内容（经 edit plan guard 拦截后）：
```
现在实施所有改动。先改三个源文件，再更新测试。

确认执行修改？
```

**问题**：用户无法做出知情决策--没有搜索发现、没有文件路径、没有改动细节、没有因果分析。

### 根因分析（5 个，按严重程度排列）

#### 根因 1：`[ANALYSIS MODE]` 约束不持久

`loop.go:596-600` 将约束作为 `pendingPinnedMessage` 注入。`turn.go:67-70` 在第一轮 turn 后清空 `pendingPinnedMessages`：

```go
for _, pm := range e.pendingPinnedMessages {
    messages = append(messages, ModelMessage{Role: "user", Content: pm})
}
e.pendingPinnedMessages = nil  // 只活一轮
```

后果：agent 在第一轮看到"不要修改代码"，做了几轮搜索后约束消失，直接跳到实现。

#### 根因 2：没有引擎级"分析→报告→等待确认"闸门

系统提示要求"先输出发现，然后询问用户是否要修改"，但这是纯文本指令，引擎层无代码强制执行。edit plan guard（`turn.go:310-351`）在 agent 已发出 edit/write 调用时才拦截，触发太晚。

#### 根因 3：edit plan summary 缺少结构化细节

`formatEditPlanSummary`（`turn.go:1340-1355`）只展示 `plan.Reasoning`（agent 的简短文本）+ "确认执行修改？"。`plan.Edits`（含路径、旧文本、新文本）完全未展示。

#### 根因 4：意图分类器无法处理"分析+实现"混合请求

用户输入同时含分析动词和修改意图。分类器只能选一个：
- `analyze`：约束只活一轮（根因 1）
- `continue`：无约束，直接实现

#### 根因 5：搜索阶段中间文本被清除

`turn.go:191-193`：当 tool calls 存在时，纯意图短语（如"让我搜索..."）被 `isIntermediateText` 清除。分析发现从未进入历史，edit plan guard 的 `reasoningForEditPlan` 只能找到最近的简短文本。

---

## 设计方案：方案 A（分析报告闸门 + 增强 edit plan summary）

### 架构概览

```
用户输入（含分析+修改意图）
      │
      ▼
  ┌─ 搜索阶段 ──────────────────────┐
  │  grep / read / lsp（只读工具）    │
  │  中间文本可被 isIntermediateText  │
  │  清除（不影响）                   │
  └──────────┬───────────────────────┘
             │ agent 首次发出 edit/write
             ▼
  ┌─ 分析报告闸门（新增）─────────────┐
  │  检查：runToolCallCount > 0       │
  │  且 AnalysisReportConfirmed=false │
  │                                   │
  │  拦截 edit/write，注入 user msg：  │
  │  "请先输出完整的分析报告，包括：    │
  │   1. 发现了什么                   │
  │   2. 为什么需要改                 │
  │   3. 计划改哪些文件、改什么        │
  │   然后等待用户确认。"              │
  └──────────┬───────────────────────┘
             │ agent 输出 text-only 报告
             ▼
  ┌─ stop hook 判定 ─────────────────┐
  │  text-only + RunToolCallCount > 0 │
  │  → ConclusionClassifier 判定      │
  │  → conclusion=true → Run() 结束   │
  │  → 用户看到完整分析报告            │
  └──────────┬───────────────────────┘
             │ 用户回复确认
             ▼
  ┌─ edit plan guard（增强后）────────┐
  │  AnalysisReportConfirmed=true     │
  │  展示：分析推理 + 文件路径列表     │
  │  + 每个 edit 的简要摘要            │
  │  + "确认执行修改？"                │
  └──────────┬───────────────────────┘
             │ 用户确认
             ▼
  ┌─ 执行 edit/write ────────────────┐
  │  PlanConfirmed=true               │
  │  渐进式 diff 展示                  │
  └───────────────────────────────────┘
```

### 组件设计

#### 组件 1：持久化 ANALYSIS MODE 约束（修复根因 1）

**改动文件**：`engine/types.go`、`engine/loop.go`、`context/builder.go`

**设计**：
- 在 `TaskState` 新增字段 `AnalysisMode bool`（JSON 序列化，跨 turn 持久）
- `loop.go:591-602`：`IntentAnalyze` 分支不再注入 `pendingPinnedMessage`，改为设置 `e.state.AnalysisMode = true`
- `context/builder.go`：`Build` 时检查 `state.AnalysisMode`，若为 true 则在 messages 末尾注入 `[ANALYSIS MODE]` 约束文本
- 每轮 turn 都能看到约束，不限于第一轮

**接口**：
```go
// types.go - TaskState 新增字段
type TaskState struct {
    // ...existing fields...
    AnalysisMode bool `json:"analysis_mode"` // 持久化分析模式约束
}
```

```go
// context/builder.go - Build 中注入
if state.AnalysisMode {
    constraint := "[ANALYSIS MODE] 用户要求仅进行分析..."
    messages = append(messages, ModelMessage{Role: "user", Content: constraint})
}
```

**何时清除 `AnalysisMode`**：用户下次发消息时，`detectUserIntent` 重新分类。若为 `IntentContinue`（用户说"改吧"）或 `IntentNewTopic`，清除 `AnalysisMode`。

#### 组件 2：分析报告闸门（修复根因 2、5）

**改动文件**：`engine/types.go`、`engine/turn.go`、`engine/loop.go`

**设计**：
- 在 `TaskState` 新增字段 `AnalysisReportConfirmed bool`
- 在 `turn.go` 的 edit plan guard 之前（约 line 310）插入新检查：

```go
// Analysis report gate: before allowing edit/write, require the agent
// to present a text-only analysis report and get user confirmation.
if e.runToolCallCount > 0 && !e.state.AnalysisReportConfirmed &&
    !e.state.PlanConfirmed && e.pendingEditPlan == nil {
    var editCalls []ToolCallRequest
    for _, call := range calls {
        if call.Name == "edit" || call.Name == "write" {
            editCalls = append(editCalls, call)
        }
    }
    if len(editCalls) > 0 {
        // Block: inject a user message asking for analysis report
        nudgeMsg := "在修改代码之前，请先输出完整的分析报告：\n" +
            "1. 你发现了什么（列出具体位置和代码）\n" +
            "2. 为什么需要修改\n" +
            "3. 计划改哪些文件、怎么改\n" +
            "输出报告后停止，等待用户确认再执行修改。"
        if !e.isChinese {
            nudgeMsg = "Before making changes, output a complete analysis report:\n" +
                "1. What you found (list specific locations and code)\n" +
                "2. Why changes are needed\n" +
                "3. Which files you plan to change and how\n" +
                "Stop after the report and wait for user confirmation before modifying."
        }
        // Store the blocked calls for later execution
        e.history = append(e.history, assistant)
        for _, c := range calls {
            e.history = append(e.history, Message{
                Role:       "tool",
                ToolCallID: c.ID,
                Content:    "Blocked: " + nudgeMsg,
                Timestamp:  time.Now(),
            })
        }
        e.pendingAnalysisNudge = true
        return TurnResult{Done: false, FinishReason: finish}, nil
    }
}
```

- Agent 收到 nudge 后输出 text-only 分析报告
- Stop hook 判定为 conclusion → `TurnResult{Done: true}` → Run() 结束 → 用户看到报告
- 用户回复确认后，`loop.go` 的 `Run()` 入口检测到用户确认消息，设置 `AnalysisReportConfirmed = true`
- 下一轮 Run() 中，edit plan guard 正常工作（闸门已通过）

**何时清除 `AnalysisReportConfirmed`**：每次新 Run() 开始时，若意图为 `IntentNewTopic` 或 `IntentAnalyze`，重置为 false。

**用户确认的检测**：复用现有 `isDangerousConfirmation` 逻辑（loop.go:433）。当 `pendingAnalysisNudge == true` 且用户消息是确认时，设置 `AnalysisReportConfirmed = true` 并清除 `AnalysisMode`。

#### 组件 3：增强 edit plan summary（修复根因 3）

**改动文件**：`engine/turn.go`

**设计**：修改 `formatEditPlanSummary` 展示结构化信息：

```go
func formatEditPlanSummary(plan *PendingEditPlan, zh bool, cwd string) string {
    var sb strings.Builder

    // Step 1: Reasoning (WHY)
    if reasoning := plan.Reasoning; reasoning != "" {
        sb.WriteString(reasoning)
        sb.WriteString("\n")
    }

    // Step 2: File changes list (WHAT)
    if len(plan.Edits) > 0 {
        if zh {
            sb.WriteString(fmt.Sprintf("\n### 涉及 %d 个文件的修改：\n", len(plan.Edits)))
        } else {
            sb.WriteString(fmt.Sprintf("\n### %d file(s) to modify:\n", len(plan.Edits)))
        }
        for i, edit := range plan.Edits {
            path := relPath(edit.Path, cwd)
            if edit.Tool == "write" {
                if zh {
                    sb.WriteString(fmt.Sprintf("%d. **%s** (写入 %d 字符)\n", i+1, path, len(edit.NewText)))
                } else {
                    sb.WriteString(fmt.Sprintf("%d. **%s** (write %d chars)\n", i+1, path, len(edit.NewText)))
                }
            } else {
                // edit: show brief old -> new preview
                oldPreview := truncateStr(strings.TrimSpace(edit.OldText), 60)
                newPreview := truncateStr(strings.TrimSpace(edit.NewText), 60)
                if zh {
                    sb.WriteString(fmt.Sprintf("%d. **%s**\n   `%s` → `%s`\n", i+1, path, oldPreview, newPreview))
                } else {
                    sb.WriteString(fmt.Sprintf("%d. **%s**\n   `%s` -> `%s`\n", i+1, path, oldPreview, newPreview))
                }
            }
        }
    }

    // Step 3: Confirmation
    if zh {
        sb.WriteString("\n确认执行修改？")
    } else {
        sb.WriteString("\nProceed with the changes?")
    }
    return sb.String()
}
```

### 数据流

```
用户消息 "看下...找出来...需要替换"
      │
      ▼ detectUserIntent → IntentAnalyze
      │
      │ state.AnalysisMode = true （持久化到 TaskState）
      │ state.AnalysisReportConfirmed = false
      │
      ▼ Run() 循环开始
      │
      │  Turn 1-3: grep/read/lsp 搜索（context.Build 每轮注入 ANALYSIS MODE）
      │  runToolCallCount += 每轮的工具数
      │
      ▼ Turn 4: agent 发出 edit/write
      │
      │  分析报告闸门检查：
      │    runToolCallCount > 0 ✓
      │    AnalysisReportConfirmed == false ✓
      │    → 拦截，注入 nudge："请先输出分析报告"
      │    → pendingAnalysisNudge = true
      │
      ▼ Turn 5: agent 输出 text-only 分析报告
      │
      │  无 tool calls → stop hook 触发
      │  ConclusionClassifier 判定 → conclusion=true
      │  → TurnResult{Done: true}
      │  → Run() 结束
      │  → 用户看到完整分析报告
      │
      ▼ 用户回复 "确认，改吧"
      │
      │  loop.go Run() 入口：
      │    isDangerousConfirmation("确认，改吧") = true
      │    pendingAnalysisNudge == true
      │    → state.AnalysisReportConfirmed = true
      │    → state.AnalysisMode = false
      │
      ▼ Run() 循环继续
      │
      │  Turn 6: agent 再次发出 edit/write
      │  分析报告闸门：AnalysisReportConfirmed == true → 跳过
      │  edit plan guard：展示增强后的 summary（路径+摘要+确认）
      │  → 用户确认
      │
      ▼ Turn 7+: 执行 edit/write，渐进式 diff
```

### 错误处理

1. **Agent 无视 nudge，再次发出 edit/write**：闸门再次拦截（`AnalysisReportConfirmed` 仍为 false）。最多拦截 2 次（复用 stop hook retry 机制），之后允许 edit plan guard 接管（降级为当前行为）。

2. **Stop hook 误判分析报告为非结论**：`ConclusionClassifier` 返回 false → nudge agent 继续输出。Agent 会补充更多内容。若 2 次后仍非结论 → `Exhausted` → 返回 Blocked，用户看到诊断信息。

3. **用户不确认分析报告而直接提新问题**：`detectUserIntent` 分类为 `IntentNewTopic` 或 `IntentAnalyze` → 重置 `AnalysisReportConfirmed = false` 和 `AnalysisMode`。

### 测试策略

1. **持久化约束测试**：`AnalysisMode=true` 时，`context.Build` 输出包含 `[ANALYSIS MODE]` 约束
2. **闸门拦截测试**：`runToolCallCount > 0` + `AnalysisReportConfirmed=false` + edit 调用 → 拦截 + 注入 nudge
3. **闸门放行测试**：`AnalysisReportConfirmed=true` → 闸门不拦截，edit plan guard 正常工作
4. **用户确认测试**：`pendingAnalysisNudge=true` + 确认消息 → `AnalysisReportConfirmed=true` + `AnalysisMode=false`
5. **增强 summary 测试**：`formatEditPlanSummary` 输出包含文件路径和 edit 摘要
6. **混合意图测试**：用户输入含分析+修改 → 意图分类 + 闸门配合工作

### 涉及文件清单

| 文件 | 改动类型 | 说明 |
|------|----------|------|
| `engine/types.go` | 新增字段 | `AnalysisMode`、`AnalysisReportConfirmed` |
| `engine/loop.go` | 修改 | 意图分类后设置 `AnalysisMode`；用户确认时设置 `AnalysisReportConfirmed` |
| `engine/turn.go` | 修改 | 新增分析报告闸门检查；增强 `formatEditPlanSummary` |
| `context/builder.go` | 修改 | `Build` 时注入持久化的 `[ANALYSIS MODE]` 约束 |

### 范围排除（YAGNI）

- **不修改意图分类器逻辑**：现有 `analyze/continue/new_topic` 三分类已足够，闸门在引擎层补充分类器无法覆盖的混合请求场景
- **不修改 stop hook 逻辑**：现有 `ConclusionClassifier` 能正确判定分析报告是否为结论
- **不增加新的 TaskState 持久化机制**：复用现有 JSON 序列化
- **不修改 UI 层**：edit plan summary 仍通过 `TurnResult.Questions` 传递
