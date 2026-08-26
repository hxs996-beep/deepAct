# 会话恢复（/resume）+ AGENTS.md 加载

日期：2026-08-26
状态：已批准（A1 持久化方案 1 + 恢复窗口 A + token 预算 A；A2 项目根+用户级级联 B）

## 背景与问题

DeepAct 当前存在两个与社区（Pi / Claude Code）对齐的缺口：

1. **会话不可恢复**：`~/.deepact/sessions/*.jsonl` 只持久化 `user_message` 与生命周期事件
   （`max_turns`/`act_complete`/`verify_compact`）；assistant 消息与工具结果只存在于内存
   `e.history`，每次启动 `SessionID` 全新（`cmd/run.go:196`），无法继续之前的对话。
2. **无 AGENTS.md 加载**：项目约定（Claude Code/Pi 的 `AGENTS.md` 惯例）无法注入上下文，
   用户每次要手动贴约定。归档设计 `docs/archive/DESIGN.md:920` 有草案但从未实现。

## 目标

1. TUI 内 `/resume` 弹出会话选择器，选择后以 **continue 语义**（沿用原会话 ID，追加写入）继续。
2. 恢复时重放 **用户 + assistant 文本流**（工具结果摘要化，恢复时剥离工具链）。
3. 启动时读取项目根 `AGENTS.md` 与 `~/.deepact/AGENTS.md`，级联注入稳定区。

## 方案决策

### A1 会话恢复

- **持久化机制**：扩展现有 JSONL 会话文件，新增 `message` 事件（方案 1）。
  - 不做独立 transcript 文件（方案 2）：职责分离代价高、双文件心智混乱。
  - 不做 Run 结束快照覆盖（方案 3）：只留最近一轮，不满足"重放完整对话"。
- **增长控制**：仅恢复窗口，不做日志清理（用户选 A）。
  - 恢复只重放最近 `keepRecentTokens`（默认 16K，对齐 `compressor.go:44` 的 `tailBudget=16384`）。
  - 会话文件继续 append（现状即 append），磁盘有天然上限兜底，清理留待需要时。
- **恢复窗口计量**：按 token 预算（用户选 A），从尾部向前累加，**只允许在 user 消息边界切割**。
- **恢复语义**：剥离工具链——丢弃 `ToolCalls` 字段并跳过 `tool` 消息，只重放 `user`+`assistant` 文本流。
  - 规避 API 契约问题（`assistant(tool_calls)` 后必须紧跟 `tool` 消息，`loop.go:512`）。
  - 语义上"继续对话"不需要旧工具执行细节，token 开销最小。

### A2 AGENTS.md 加载

- **位置**：项目根 `AGENTS.md` 首选 + `~/.deepact/AGENTS.md` 次之（用户确认，沿用原位置思维，
  只是让 deepact 真正读取 AGENTS.md）。
- **语义**：级联叠加（用户选 B，与社区一致）——用户级 + 项目级都注入，项目级在后（优先级更高）。
- **注入位置**：稳定区（Block S）——与 OS/目录树同属 prefix-cache 友好区，启动时定死。

## 数据流

### A1 持久化（写入）

```
Engine.Run() 收尾
   │  defer persistHistory()        ← 单一出口，覆盖 ~20 个 return 路径
   ▼
遍历 e.history[runStartHistoryLen:]
   │  user      → 全文 Message
   │  assistant → 全文 Message（含 ToolCalls 字段，供审计）
   │  tool      → 仅 briefDigest 摘要（80 字符首行+行数，turn.go:1254）
   ▼
emitEvent("message", StageAct, msg)  →  AppendEvent → session-<id>.jsonl
```

- `reasoning_content` 不落盘（不参与计费、无需恢复）。
- `message` 事件附带 `WorkDir` 字段（当前 cwd），供选择器按项目分组展示。

### A1 恢复（读取 + 预载）

```
/resume
  → 显示选择器（当前 cwd 的会话列表，经 runner.ListSessions() 查询）
  → 用户选 session-X
  → LoadEvents() 过滤 message 事件 → []Message
  → 按 keepRecentTokens 从尾向前裁剪（user 边界切割）
  → 剥离 ToolCalls、跳过 tool 消息
  → engine.SetSessionID("session-X")        // continue：后续事件写入该文件
  → engine.SetHistory(trimmedMessages)      // 首次 Run() 前预载
  → engine.Run(prompt)
```

- engine 是 `sync.Once` 惰性创建（`ui/runner.go:92 getEngine`），`SetSessionID`/`SetHistory`
  在首次 `Run()` 前调用即可。
- TUI 预填 `m.messages` 为用户+assistant 文本流，显示一行 system 提示「已恢复会话 <id>」。

### A2 注入

```
启动（cmd/run.go buildEngineDeps）
   │  LoadAgentsMD(workDir)
   │    ├─ ~/.deepact/AGENTS.md    → 用户级（低优先）
   │    └─ <projectRoot>/AGENTS.md → 项目级（高优先）
   ▼
ContextAssembler.SetAgentsBlock(rendered)   // 缓存，启动后不变
   ▼
Build() 时并入 BuildStableSessionContext   // Block S 的 "## Project Conventions" 段
```

## 数据模型

### engine/types.go — Message 事件（新增）

`engine.Message` 已具备全部 JSON tag（`role`/`content`/`tool_calls`/`tool_call_id`/`reasoning_content`/`timestamp`），
直接作为 `message` 事件 payload。会话事件 `Event{SessionID, Type, Stage, Timestamp, Payload}` 复用。

新增事件类型常量：

```go
const EventTypeMessage = "message" // payload: engine.Message（tool 消息为 briefDigest 摘要）
```

会话 WorkDir 记录：扩展 `Event` 增加 `WorkDir` 字段（选扩展 Event 而非封装 payload，避免破坏现有
JSONL 读取）。旧会话无该字段 → 归为"未知目录"。

### engine/loop.go — 新增方法与字段

```go
// 新增
func (e *Engine) SetSessionID(id string)     // 覆盖 config.SessionID（continue 语义）
func (e *Engine) SetHistory(h []Message)     // 首次 Run() 前预载恢复的历史

// Run() 收尾 defer
func (e *Engine) persistHistory()            // 把 e.history[runStartHistoryLen:] 落盘为 message 事件
```

### context/agents.go — 新增

```go
type AgentsFile struct {
    Source  string // "~/.deepact/AGENTS.md" 或 "<projectRoot>/AGENTS.md"
    Content string
}

func LoadAgentsMD(projectRoot string) []AgentsFile
```

### ui/runner.go — EngineRunner 接口扩展

```go
type EngineRunner interface {
    // ... 现有方法
    SetSessionID(id string)
    SetHistory(messages []engine.Message)
    // 会话列表查询（供 /resume 选择器）：返回当前 cwd 的会话摘要
    ListSessions() []SessionSummary
}
```

`ProgressEngineRunner` 与 `DefaultEngineRunner` 都实现。`SessionSummary` 为 ui 层轻量结构
（ID/UpdatedAt/首条消息摘要），由 runner 内部调用 session.Store，避免 ui 直接依赖 session 包。

## UI 组件（ui/model.go）

- 新增状态 `stateResume`（AppState 枚举）。
- `slashCommands` 列表新增 `{Command: "/resume", Args: "", Description: "恢复之前的会话"}`。
- `/resume` 为本地命令（不触发 engine，仿 `/help`），进入 `stateResume` 渲染选择器覆盖层。
- 选择器交互：`↑/↓` 导航、`Enter` 恢复、`Esc` 取消。
- 恢复后预填 `m.messages`：先追加 system「已恢复会话 <id>」，再追加 user+assistant 文本流。

## 错误处理

- **AGENTS.md 读取失败**：`log.Printf` 警告，不阻塞启动。
- **无 AGENTS.md**：`agentsBlock` 为空，输出与现状完全一致（零回归）。
- **会话文件损坏/解析失败**：`/resume` 跳过该会话并警告，不崩溃。
- **恢复窗口为空**：按新会话处理（无历史）。

## 测试计划

### context/agents_test.go（新增）

- 级联顺序：用户级 + 项目级都注入，项目级在后。
- 用户级缺失 / 项目级缺失 / 两者都缺失 / 空文件。
- 中文/英文内容原样透传。

### engine/loop_persist_test.go（新增）

- Run 结束后 `message` 事件写入：user 全文、assistant 全文、tool 摘要。
- 多次 Run 各落盘一次（`runStartHistoryLen` 边界正确）。
- `reasoning_content` 不落盘。

### engine/loop_resume_test.go（新增）

- `LoadEvents` → 过滤 message → 裁剪到 keepRecentTokens（user 边界切割）。
- 裁剪后剥离 ToolCalls、跳过 tool 消息，输出纯 user+assistant 文本流。
- `SetHistory` + `SetSessionID` 预载后首次 Run 正常。

### ui/resume_test.go（新增）

- `/resume` 进入 stateResume，选择器列出会话。
- Enter 恢复、Esc 取消。

## 影响范围

- **修改文件**：`engine/types.go`、`engine/loop.go`、`ui/runner.go`、`ui/model.go`、`cmd/run.go`、
  `context/builder.go`、`session/store.go`（WorkDir 记录）
- **新增文件**：`context/agents.go`、`context/agents_test.go`、`engine/loop_persist_test.go`、
  `engine/loop_resume_test.go`、`ui/resume_test.go`

## 未纳入范围

- 会话删除 / 清理入口（YAGNI，增长有天然上限）。
- 恢复会话的跨目录浏览（仅当前 cwd）。
- HTML 导出 / gist 分享。
- `deepact -c` / `-r` CLI 标志（本次仅 `/resume`）。
