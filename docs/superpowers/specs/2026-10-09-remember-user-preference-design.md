# 用户偏好沉淀（remember_preference）设计

日期：2026-10-09

## 问题

DeepAct 的长期约定只有 `AGENTS.md` 一条通道，且完全靠手工维护。用户不仅用它写代码，也会用来剪视频、写文档；这类非仓库任务里的习惯一旦没写进 `AGENTS.md`，就会变成每轮重复提醒（"别这么做""要这样写"）。现有 `memory/` 存的是任务结论（`MemoryMarkers`/`Decisions`/…），不是用户操作偏好，且没有从历史会话自动抽取的能力——两者都接不上这个痛点。

业界参考：codex 用一条后台两阶段记忆管线（`codex-rs/memories/`）自动从 rollout 抽取 "Stable user operating preferences"，合并进 `~/.codex/memories/`，其中 `memory_summary.md` 常驻系统提示。本设计取"沉淀用户偏好"这一目标，但改为**对话内半自动确认**，不引入后台挖掘管线。

## 目标与非目标

- 目标：让用户"又提醒了一次"的稳定偏好，经一次确认后沉淀进项目 `AGENTS.md`，下次会话自动生效，减少重复提醒。
- 非目标：后台自动挖掘历史会话（codex 路线）；跨项目用户级存储（曾被讨论，最终否决）；重写 AGENTS.md 的加载/注入机制。

## 决策记录

| 维度 | 结论 |
|---|---|
| 触发 | 半自动确认：agent 识别到"用户重复纠正/提醒"，用 `ask_user` 征询，确认后才写 |
| 载体 | 当前项目根下的 `AGENTS.md` |
| 位置 | 固定小节 `## 用户偏好` |
| 实现路线 | 提示词引导 + 专用工具 `remember_preference`（而非纯提示词或复用 memory 通道） |

否决项：
- 纯提示词（模型用 `edit`/`write` 手搓）：文件定位、小节创建、去重、格式一致性都交给模型，脆弱。
- 复用 memory 通道再投影：多一套状态与同步，且用户明确要写 `AGENTS.md`。
- 用户级 `~/.deepact/` 存储：用户中途改判为项目级。
- marker 注释 + `AGENTS.override.md` 优先级处理：用户要求简化，固定标题、固定写项目根 `AGENTS.md`。

## 设计

### 1. 工具 `remember_preference`

`tools/builtin/remember_preference.go`，与 `write`/`edit` 同构。

- 参数：`preference`（string，必填）。
- 描述中声明：用于把**稳定**偏好追加到项目根 `AGENTS.md` 的 `## 用户偏好` 小节并去重；**只在用户明确同意后调用**（通常先经 `ask_user`）；一次性、任务特定指令不要记；新条目下次会话生效。

### 2. 落点解析

`context` 包导出 `ProjectAgentsMDPath(cwd)`：复用 `projectAgentsMDDirs` 的既有规则——cwd 或最近含 `.git` 的祖先为项目根，找不到退化为 cwd；返回 `filepath.Join(root, "AGENTS.md")`。写端与读端共用同一规则，避免写出不会被加载的副本。

### 3. 文件格式与去重

```markdown
## 用户偏好

- <偏好文本>
```

`upsertPreference`（纯函数，便于单测）三情形：

- 无文件/无小节 → 末尾追加小节；
- 有小节 → 在小节内最后一条非空行之后插入，保持连续列表；
- 小节内已有归一化后相同的条目 → 返回未变更（no-op）。

归一化 = `strings.Fields` 折叠空白；小节边界 = 下一条 `#` 起始行。写入用 temp + rename 原子落盘，权限 `0o644`。

### 4. 提示词引导

`context/promptset/zh/system.md` 新增顶级小节 `# 用户偏好沉淀（半自动）`：触发信号（重复纠正/同类二次要求）、动作（先 `ask_user` 给"记下/不用"两选项）、只有确认后才调工具、只记稳定偏好、成功后一句话告知且"下次启动生效"。

### 5. 注册

`cmd/run.go` 的 `registerBuiltinTools` 注册 `builtin.NewRememberPreferenceTool()`。

### 6. 生效时机（已知取舍）

`AGENTS.md` 在会话启动时加载并缓存进稳定前缀（`SetAgentsBlock`）。本次会话内新记的条目**下次启动才自动生效**；工具 digest 内已明示，避免模型误以为本会话已读取。热更新稳定块会破坏 DeepSeek 前缀缓存，明确不做。

## 测试

- `tools/builtin/remember_preference_test.go`：创建文件与小节、追加到已有小节、空白归一化去重、写项目根（`.git` 祖先）、空输入拒绝。
- `context/agents_test.go::TestProjectAgentsMDPath`：祖先 `.git` 命中、无标记退化为 cwd、空 cwd 返回空串。

## 验证

`gofmt -l`（clean）、`go build ./...`、`go vet ./...`、`go test ./...` 全部通过。
