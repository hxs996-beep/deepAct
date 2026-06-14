# 前缀缓存命中率优化方案

## 问题

DeepSeek 前缀缓存命中率极低（~10%）。根因：Block B（volatile task state）夹在稳定前缀和 history 之间。Block B 的 `turn_number` 每轮递增，导致之后全部 history（包括上轮相同内容）变成 cache miss。

## 目标结构

```
请求 N:   [0:system] [1:block_s+env] [2:repo] [3..K:turn-blocks] [K+1..M:history全部] [M+1:volatile] [M+2:reminder]
请求 N+1: [0:system] [1:block_s+env] [2:repo] [3..K:turn-blocks] [K+1..M:history全部] [M+1..M+Δ:本轮新增] [M+Δ+1:volatile] [M+Δ+2:reminder]
                                                                     └─ 完全相同 → 缓存命中 ─┘ └─ 新内容 ─┘ └── 尾部小变化 ──┘
```

## 改动 1: context/prompt.go — 环境信息从 Block B 移到 Block S

### BuildStableSessionContext 签名变更

```go
// 之前
func BuildStableSessionContext(agentsMD string, userLang string) string

// 之后
func BuildStableSessionContext(agentsMD string, envInfo EnvironmentInfo, userLang string) string
```

### BuildStableSessionContext 追加环境块

在 AGENTS.md 之后、Language 之前插入：

```go
builder.WriteString("## Environment\n")
builder.WriteString(fmt.Sprintf("- OS: %s\n", envInfo.OS))
builder.WriteString(fmt.Sprintf("- Arch: %s\n", envInfo.Arch))
builder.WriteString(fmt.Sprintf("- CWD: %s\n", envInfo.CWD))
if envInfo.Model != "" { ... }
if envInfo.Date != "" { ... }
builder.WriteString("\n")
```

### BuildBlockB 删除环境段

```go
// 之前
func BuildBlockB(env EnvironmentInfo, taskState string) string { ... }

// 之后
func BuildBlockB(taskState string) string {
    // 只有 "## Task State (verbatim)" 部分
}
```

## 改动 2: context/builder.go — 结构调整 + 去 FreshTurns 分割

### 2a. ContextAssembler 添加 envInfo 缓存

```go
type ContextAssembler struct {
    // ... 现有字段
    envInfo EnvironmentInfo  // 新增：session 启动时构建一次
}
```

### 2b. NewContextAssembler 构建 envInfo

```go
func NewContextAssembler(projectRoot string, estimator *llm.TokenEstimator) *ContextAssembler {
    // ...
    return &ContextAssembler{
        // ... 现有字段
        envInfo: buildEnvironmentInfo(),
    }
}
```

### 2c. Build() 方法重构

将 stableSessionBlock 构建改为传入 envInfo：

```go
if a.stableSessionBlock == "" {
    a.stableSessionBlock = BuildStableSessionContext(a.agentsMD, a.envInfo, a.userLang)
}
```

Block B 调用去掉 env 参数：

```go
blockB := BuildBlockB(formatTaskStateVolatile(state))
```

去掉 FreshTurns 分割，history 全部放在 volatile 之前：

```go
// 去掉:
// freshStart := len(history) - engine.FreshTurns*3
// 两个 for 循环

// 改为:
for _, msg := range history {
    messages = append(messages, mapMessage(msg))
}

// 然后才是 volatile:
messages = append(messages, engine.ModelMessage{Role: "user", Content: blockB})
```

### 2d. formatTaskStateVolatile 精简字段

去掉已被 turn-blocks / reminder 覆盖的冗余字段：

```go
volatile := struct {
    ConsecutiveFails int                     `json:"consecutive_failures"`
    EditScopeFiles   int                     `json:"edit_scope_files"`
}
```

去掉: `WorkingSet`, `ModifiedFiles`, `FileCollapse`, `CallChain`, `ParentContext` (ParentBoard struct removed entirely)

## 改动 3: engine/turn.go — 每轮写入 AccumulatedBlocks

在 `executeTurn()` 的 `return result, nil` 之前追加：

```go
// 每轮结束：将本轮的文件发现收敛到前缀区
var filesRead []string
var filesSearched []string
for _, c := range regularCalls {
    path := extractPathFromArgs(c.Input)
    if path == "" {
        continue
    }
    switch c.Name {
    case "read":
        if !containsString(filesRead, path) {
            filesRead = append(filesRead, path)
        }
    case "grep", "glob":
        if !containsString(filesSearched, path) {
            filesSearched = append(filesSearched, path)
        }
    }
}
block := context.FormatTurnBlock(
    e.state.TurnNumber,
    filesRead,
    filesSearched,
    e.state.ModifiedFiles,
    e.state.MemoryMarkers,
    nil,
)
if block != "" {
    e.state.AccumulatedBlocks = append(e.state.AccumulatedBlocks, block)
}
```

需要在 turn.go 头部添加 `"github.com/deepact/deepact/context"` import。

## 改动汇总

| 文件 | 改动量 | 描述 |
|---|---|---|
| `context/prompt.go` | +18 / -18 行 | BuildStableSessionContext 加 env 参数；BuildBlockB 删 env |
| `context/builder.go` | +8 / -20 行 | 加 envInfo 字段；去 FreshTurns 分割；精简 volatile |
| `engine/turn.go` | +25 行 | 每轮写入 AccumulatedBlocks |

## 预期效果

- 可缓存前缀从 ~10K 扩展到 ~(10K + 全部 history 已有部分)
- 当 history 占总 token 80% 时，命中率从 ~12% → ~88%
- 尾部 volatile ~200 tokens 每轮仍 cache miss，可忽略

## Trade-off

- 去掉 FreshTurns 的"最近消息置底"策略 — 由 AccumulatedBlocks 每轮摘要弥补 recency
