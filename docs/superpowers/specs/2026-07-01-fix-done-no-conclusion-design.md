# Fix: "Done" + 文件列表但无结论

## 问题

用户执行分析任务后，UI 显示 "Done" 和已读文件列表，但没有实际分析结论。

### 根因

三个机制叠加：

1. **`renderToolSummary`**（`ui/model.go:1922`）无条件下输出 `● Done (N tools, M files modified)` —— "Done" 指工具执行完毕，但展示为对话消息，看起来像 AI 的结论。

2. **`buildRunSummary`**（`engine/loop.go:788`）逆序遍历 history 取最后一条 assistant content 作为 summary，不判断内容质量。如果模型输出 "Done" 或回显了 Block B 的 `read_history`，这就是用户看到的"结论"。

3. **`stripInternalPromptEcho`**（`engine/dsml.go:146`）剥离内部提示回显的规则不完整，模型以非标准措辞回显文件列表时剥离不掉。

## 方案

### C1：`buildRunSummary` 质量门槛

**文件：`engine/loop.go`**

在 `stripDSMLTokens` 之后、返回之前插入质量检查：

```go
summary = stripDSMLTokens(summary)
if summary != "" && !isSubstantiveSummary(summary) {
    summary = ""
}
```

新增 `isSubstantiveSummary` 函数，三项判断：

| 规则 | 不通过条件 |
|------|-----------|
| 长度门槛 | 纯 ASCII < 20 字符 且 中文 < 10 字符 |
| 空壳词 | 纯为 "Done"/"完成"/"OK"/"好的"/"I'm done" 等 |
| 文件列表回声 | 以路径模式（`- /`、`[<>]`）开头的行 ≥ 总行数 50% |

不通过时 fallback 到现有诊断字符串（`（本轮未生成回复文本，已执行 N 次工具调用）`）。

### C2：`renderMessage` toolsummary 弱化样式

**文件：`ui/model.go`**

`toolsummary` role 使用 `DimStyle` 渲染，与 assistant 的 markdown 渲染形成视觉层级。不改 `finishStreaming` 逻辑（引擎层已过滤空壳 summary）。

### C3：`renderToolSummary` 去 "Done"

**文件：`ui/model.go`**

```
- ● Done (N tools, M files modified)
+ ● N tools executed, M files modified
```

## 影响范围

| 文件 | 改动 |
|------|------|
| `engine/loop.go` | `buildRunSummary` + 新增 `isSubstantiveSummary` |
| `engine/loop_summary_test.go` | 新增 `isSubstantiveSummary` 测试用例 |
| `ui/model.go` | `renderToolSummary` 文本 + `renderMessage` toolsummary 样式 |

无公开接口变更，无新增依赖。
