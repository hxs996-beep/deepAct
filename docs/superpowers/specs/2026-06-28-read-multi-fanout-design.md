# read_multi：一次 LLM 调用扇出多方向读取 — 设计规格

- 日期：2026-06-28
- 分支：feat/agent-teams
- 范围：tools/builtin（新工具）、engine/turn.go（ReadHistory + loop-guard 交互）、context/（提示引导）

## 1. 背景与动机

引擎早已支持"一次 LLM 调用 → 多个工具调用并行执行"：`engine/turn.go` 把 LLM 返回的多个 `tool_calls` 收集后，read-only 类（read/grep/glob/lsp/bash）经 `tools/registry.go` 的 WaitGroup 并行执行，destructive 类（edit/write）顺序执行。

但实际观察到的痛点是 **LLM 不愿意在一轮里吐出多个饱满的 read**——它倾向一次只发一个窄 read（小 offset/limit 或单 symbol），串行往返多次，导致"读得太碎、返回太少"，轮次昂贵。

根因不在执行层（并行基础设施已有），而在工具语义层：没有"扇出探索"的明确工具语义，LLM 缺乏"一次列多目标"的抓手。本设计新增 `read_multi` 复合工具，让 LLM 一次调用即可并行读取 ≤8 个目标，单轮信息吞吐更高、往返更少。

**与 `read-loop-prevention` 设计（同日）的关系**：互补。read-loop-prevention 治"反复读同一区段"；read_multi 治"一轮读得太少太碎"。两者共用同一套 scope-aware 口径与 ReadHistory 记账，read_multi 不得成为绕过 loop 守卫的后门。

## 2. 设计原则

- **复用而非重写**：抓取逻辑复用 `read` 的内部函数，口径一致。
- **隔离**：`read_multi` 为独立工具，不污染已稳定的 `read` 路径。
- **不设全局返回上限**：靠 compressor 兜底；单 target 仍受 `maxReadBytes`/`maxReadTokens` 约束。
- **显式多目标**：LLM 显式列目标，不做自动 LSP 扇出（与 `lsp` 工具职责不重叠）。
- **YAGNI**：不做"自动合并连续单 read"改写、不强制使用场景。

## 3. 工具 schema 与语义

新增 `read_multi`，定义在 `tools/builtin/read_multi.go`。

**输入 schema：**

```json
{
  "type": "object",
  "properties": {
    "targets": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "path":   {"type":"string"},
          "symbol": {"type":"string","description":"Go symbol name; when set, offset/limit ignored"},
          "offset": {"type":"integer","description":"1-based start line"},
          "limit":  {"type":"integer"}
        },
        "required": ["path"]
      },
      "maxItems": 8,
      "minItems": 1
    }
  },
  "required": ["targets"]
}
```

**语义：** 一次扇出读取 ≤8 个目标，引擎并行抓取，聚合成单个 tool 结果返回。每个 target 的抓取逻辑直接复用 `read` 的内部函数（`readSymbol` / offset-limit 切片 / `maxReadBytes` 与 `maxReadTokens` 截断口径），保证与单读一致。

**与单 `read` 的关系：** 不替代。当需要"一次性了解多处/多方向"用 `read_multi`；只需单文件精读仍用 `read`。

## 4. 并行抓取与聚合格式

### 4.1 复用函数抽取（纯重构）

将 `read.go` 中三段抓取逻辑抽成包内可复用函数（不改变 `read` 行为）：

- `readSymbol(safePath, symbol) (string, error)` — 已存在，直接复用。
- `readLines(safePath, offset, limit) (string, error)` — 从 `read` 的 `Run` 里抽出 offset/limit 切片段。
- `readFull(safePath) (string, error)` — 抽出裸读段（含 `maxReadBytes`/`maxReadTokens` 截断 + hint）。

`read` 的 `Run` 改为调用这三个函数，行为零变化（纯重构，由 `readwrite_test.go` 守护）。

### 4.2 并行抓取

`read_multi` 内部对每个 target 起 goroutine：`resolveSafePath` → 选抓取函数（symbol 优先，其次 offset/limit，否则 full）→ 返回 `{target, digest, err}`。用 `sync.WaitGroup` 等齐，**按 targets 输入顺序**写回（非完成顺序），保证结果稳定可读。

### 4.3 聚合格式（单个 tool 结果）

```
ReadMulti: 4 targets (parallel)
────────────────────────────────
=== [1] engine/roundtable.go [symbol:Run] ===
symbol Run (87 lines)
<内容>

=== [2] engine/guards.go [L55-85] ===
<内容>

=== [3] context/prompt.go (full, 412 lines, truncated at 25000 tokens — use read with offset for rest) ===
<内容>

=== [4] tools/builtin/read.go [symbol:missingSym] ===
ERROR: symbol not found
```

- 分隔头含序号、path、scope 标记（`symbol:X` / `L a-b` / `full`）。
- 每个 target 独立带截断 hint 与错误，互不影响——一个失败不拖垮整批。
- 不设全局上限（靠 compressor 兜底）；单 target 仍受 `maxReadBytes`/`maxReadTokens` 约束。

## 5. 与 ReadHistory / loop-guard 的交互

关键点：一个 `read_multi` 调用含多个 (path, scope)，必须**逐个记账**。

### 5.1 ReadHistory 记账

引擎层只看到"一个工具调用 + 一个聚合 digest"，无法直接拿到 sub-target 列表。采用**工具结果自描述、引擎解析**方案：`read_multi` 的 digest 头部带一段机器可读元数据（HTML 注释，对 LLM 不可见干扰小，compressor 压缩历史时能保留）：

```
<!-- read_multi targets: engine/roundtable.go::symbol:Run | engine/guards.go::L55-85 | context/prompt.go:: | tools/builtin/read.go::symbol:missingSym -->
```

`engine/turn.go` 的 `updateTaskStateFromTools` 中：若 `ToolName == "read_multi"`，解析该注释行，为每个 target 追加一条 `ReadRecord{Path, Scope}`。scope 串口径与单读完全一致：裸读 `""`、`symbol:X`、`L a-b`。**降级**：若注释行缺失或格式不符（旧版结果 / 手工构造），静默跳过 ReadHistory 记账，不报错——记账是 best-effort，缺失最坏导致重复读，由 loop-guard 兜底拦截。

> 备选方案（不采用）：在 `ToolResultEnvelope` 增加 `StructuredMeta` 字段透传 sub-target 列表——更干净但跨层改 `tools`/`engine` 接口，改动面大。

### 5.2 loop-guard 记账

`LoopGuard` 现按单调用 `call` 生成一个 key。`read_multi` 一个调用含多 target，需在 `turn.go` 调用 `guards.loop.Check` 处特判：若 `call.Name == "read_multi"`，解析 input 的 `targets`，对每个 (path, scope) 调一次 `Check`，任一被拦则整批 block（block 文案列出是哪个 target 触发）。复用 `guards.go` 的 scope 提取逻辑，口径与 §5.1 一致。

### 5.3 与 read-loop-prevention 设计的关系

- `read_multi` 的每个 sub-target 计入"同一 (path,scope) 累计次数"——第 3 次 nudge、第 4 次硬拦同样适用。
- 即：用 `read_multi` 反复读同一区段也会被守卫拦，不会成为绕过 loop 检测的后门。
- `read_multi` 写入的 ReadRecord 与单读无异，提示层"已读清单"自然聚合渲染，无需特殊处理。

## 6. 提示层引导

不改工具调用执行流，仅在 `context/` 提示构建链路追加引导。

**1. `read_multi` 工具 description（随 tool spec 注入）：**

```
Read up to 8 file targets in one call, executed in parallel. Use this for fan-out
exploration: when you need to understand several files/symbols/directions at once,
list them as targets instead of issuing many single reads. Each target supports
path + optional symbol/offset/limit, same semantics as `read`. Prefer this over
chained single reads when you have 2+ independent things to look at.
```

**2. 系统提示追加一段使用指引（按会话 zh/en 切换）：**

中文版：

> **高效探索：** 当需要同时了解多处代码（多个文件、多个符号、多个方向），用 `read_multi` 一次列出所有目标并行读取，而不是一个一个串行 read。这样一轮就能拿到多方向信息，减少往返。仍优先用 `lsp`（hover/goToDefinition/workspaceSymbol）做精准定位；需要精读单文件时用 `read`。

英文版镜像。注入点复用现有系统提示拼装方式，作为"工具使用建议"段落追加。

## 7. 非目标（YAGNI）

- **不做自动扇出**：不自动 LSP 抓定义/引用/同包符号（属 `lsp` 工具职责，返回不可控）。
- **不做"自动合并连续单 read 为 read_multi"**：依赖模型听话即可，复杂度高收益不确定。
- **不强制使用场景**：仅引导，不强制。
- **不改 `read` 工具行为**：仅纯重构抽取复用函数。
- **不设全局返回上限**：靠 compressor 兜底。

## 8. 测试

### 8.1 `tools/builtin/read_multi_test.go`（新增）

- 多 target 并行抓取：3 个 target（symbol / offset-limit / full 各一），结果按输入顺序返回，分隔头正确。
- 单 target 等价于 read：1 个 target 的输出与 `read` 同输入一致。
- 部分失败容错：1 个 target symbol 不存在，其余正常返回，失败 target 标 `ERROR`，整批不报错。
- 复用函数回归：`read` 重构后 `readwrite_test.go` 现有用例仍通过（守护零行为变化）。
- maxItems 防御：9 个 target 输入返回错误。

### 8.2 engine 交互测试（扩展 `guards_test.go` 或新增）

- `read_multi` 调用后，`ReadHistory` 为每个 sub-target 追加一条 `ReadRecord`，scope 串口径正确（裸读 `""`、symbol、`L a-b`）。
- 同一 (path, scope) 经 `read_multi` 反复触发：第 3 次 nudge、第 4 次硬拦（验证不绕过 read-loop 守卫）。
- `read_multi` 含一个已触发 loop 的 target → 整批 block，文案指出触发 target。

### 8.3 提示层测试

- 系统提示含 `read_multi` 引导段落（zh/en 各一）。
- `read_multi` 的 tool spec description 出现在工具清单中。

## 9. 改动文件清单

- `tools/builtin/read_multi.go` — 新工具。
- `tools/builtin/read.go` — 抽出 `readSymbol`/`readLines`/`readFull` 复用函数（纯重构）。
- `tools/builtin/read_multi_test.go` — 新增。
- `engine/turn.go` — `updateTaskStateFromTools` 解析 `read_multi` 元数据写多条 ReadRecord；loop-guard 调用处特判多 target。
- `engine/guards.go` — 若需要，补一个按 input 解析 `read_multi` sub-target 的 helper。
- `engine/guards_test.go` — 扩展。
- `context/`（提示构建）— 追加 `read_multi` 引导段落。
