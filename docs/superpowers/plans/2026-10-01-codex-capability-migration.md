# Codex → DeepAct 能力迁移路线 实现计划

> **面向 AI 代理的工作者：** 这是一份**能力迁移路线目录**，不是单个功能的实施计划。请从中挑选**一张任务卡**，先按其"实施前必读"补齐细节（重新读码、确认落点与行号），再展开为独立实施计划（推荐 superpowers:writing-plans → subagent-driven-development）。**不要**试图一次做完多张卡。每完成一张卡后提交并回本文件勾选进度。
>
> **行号说明：** 本文件中的 `file:line` 来自只读调研与抽样复核，**会随代码变动而失效**。实施前必须重新定位（用 LSP `workspaceSymbol` / `grep`），不要直接信任行号。

**目标：** 把 Codex（OpenAI Codex CLI，Rust）中可作为"能力"移植的机制，逐项转化为 DeepAct（Go 单二进制终端编码代理）的功能，同时**明确排除**会破坏 DeepAct 轻量定位的部分。

**架构约束（一切决策的前提）：**
- DeepAct = Go 单二进制、零运行时依赖（无 Node/Python/Docker）、DeepSeek 原生。
- AGENTS.md 第一性原理：**泛化性优先**（覆盖一类问题，不枚举场景）、**能力优先**（给模型工具/通道/信息让它自主决策，而不是写死判定逻辑；固定模式仅用于安全/不可逆操作的确定性保护）。
- **判定框架：能搬的是「策略与协议」层（纯逻辑、可 Go 重写、零依赖）；不能搬的是「平台绑定」层（OS API / V8 / sqlite / 云后端 / OAuth / 系统密钥链）。**

**技术栈：** Go（现有依赖，原则上不新增第三方库）。

---

## 一、如何使用本文件

1. **选卡**：按依赖图（见第四节）从无依赖的卡开始，或按用户指定。
2. **补细节**：每张卡只给了"目标 / 证据 / 落点 / 验收标准"。动手前必须：重读 deepact 侧相关文件 → 确认落点文件与插入位置 → 判断是否需要 `ask_user` 与用户确认方案取舍。
3. **展开**：把卡展开成带具体步骤、测试先行、文件结构表的实施计划（参照 `docs/superpowers/plans/2026-09-29-subagent-run-scoped-management.md` 的格式）。
4. **回填**：完成后勾选本文件对应复选框，若结论有变化（例如调研后发现不值得做）就地改写卡片的"结论"字段，并把卡片移入"已否决"区，**保留否决理由**。

---

## 二、任务总览

状态列：`[ ]` 未开始 / `[~]` 进行中 / `[x]` 已完成 / `[-]` 已否决。

### 阶段一：机制补强（低风险、纯逻辑、可单测）

| ID | 能力 | 依赖 | 工作量 | 状态 |
|---|---|---|---|---|
| M1 | AGENTS.md 祖先遍历 + override + 字节预算 | 无 | 低 | [x] |
| M2 | `apply_patch` 多 hunk 模糊补丁工具 | 决策点 D2（已定：并存） | 中 | [x] |
| M3 | 命令策略引擎 + 审批档位（execpolicy + approval policy） | 决策点 D3（已定：config.toml + 保留 blocklist） | 中 | [x] |
| M4 | Slash 命令注册表（命令 → handler） | 无 | 低 | [x] |

### 阶段二：扩展面

| ID | 能力 | 依赖 | 工作量 | 状态 |
|---|---|---|---|---|
| M5 | 模型 provider 抽象 + provider 表 + `env_key` | 无 | 低-中 | [ ] |
| M6 | `tool_search` / 动态工具发现（延迟加载） | 无 | 中 | [ ] |
| M7 | 生命周期 hooks / notify 外部命令钩子 | M3（事件语义协同） | 中 | [ ] |
| M8 | git / 环境快照注入上下文 | 无 | 低 | [ ] |

### 阶段三：结构性升级（每项建议单独设计）

| ID | 能力 | 依赖 | 工作量 | 状态 |
|---|---|---|---|---|
| M9 | worktree 隔离工作区（解锁可写子代理并行） | 无（但设计成本高） | 中-高 | [ ] |
| M10 | context-fragments marker 化注入 | M1、M8（顺序稳定） | 中-低 | [ ] |
| M11 | MCP streamable-http transport | 无 | 中 | [ ] |
| M12 | app-server 协议最小内核（stdio JSON-RPC，供 IDE/外部客户端驱动） | 无 | 中-高 | [ ] |
| M13 | 跨会话记忆自动提炼（单阶段起步） | 无 | 中 | [ ] |
| M14 | build-info + 进程诊断快照 | 无 | 低 | [ ] |

### 阶段四：按需（P2，暂不排期）

`agent 消息板` · `agent-graph-store 拓扑持久化` · `guardian 模型化审批` · `file_search 模糊文件名` · `unified exec 本地后台会话` · `view_image`（**前置：确认 DeepSeek 视觉能力**）· `plan mode UI 徽标` · `转录视图 / 历史搜索` · `沙箱失败检测` · `进程加固` · `角色 TOML 外部化` · `external-agent-migration 的 config/memory 子集`。

这些能力在本次调研中均已取证，但**收益边际或成本偏高**，需要单独任务卡再展开。取证位置见第六节"证据索引"。

---

## 三、任务卡

### M1 — AGENTS.md 祖先遍历 + override + 字节预算

**目标：** 让嵌套/monorepo 项目里各级目录的 AGENTS.md 约定都能被加载，并支持本地覆盖与总长度上限。

**Codex 证据：**
- `codex-rs/core/src/agents_md.rs:58-116`：`load_project_instructions`，含 `project_doc_max_bytes` 字节预算、`AGENTS.override.md` 本地覆盖、默认文件名与分隔符。
- `codex-rs/core/src/agents_md.rs:192-296`：`agents_md_paths` 从 project root **逐级向下到 cwd** 收集；`find_nearest_ancestor_with_markers` 定位 project root（`.git` 等 marker）。
- `codex-rs/core/src/agents_md_manager.rs:65-144`：序列化刷新 + 快照缓存 + token 上限。

**DeepAct 现状：**
- `context/agents.go:19-36` `LoadAgentsMD` 仅两级：`~/.deepact/AGENTS.md`（低优先）+ `<projectRoot>/AGENTS.md`（高优先）。
- `context/agents.go:52-61` `RenderAgentsBlock` 渲染为稳定区段落。
- 缺：祖先目录遍历、`AGENTS.override.md`、字节/token 预算、缓存管理。

**落点：** `context/agents.go`（+ 对应测试 `context/agents_test.go`）。

**验收标准：**
- [x] 在 `<root>/a/b/c` 下启动时，collect 到 `<root>/AGENTS.md`、`<root>/a/AGENTS.md`、`<root>/a/b/AGENTS.md`、`<root>/a/b/c/AGENTS.md`（存在者）。
- [x] project root 判定使用 marker（`.git`）而非 cwd，找不到 marker 时退化为"仅 cwd + home"。
- [x] 同名 `AGENTS.override.md` 覆盖同目录 `AGENTS.md`。
- [x] 总字节超预算时按"更靠近 cwd 者优先保留"截断，且截断行为有测试。
- [x] 无文件时输出与现状**完全一致**（零回归，`RenderAgentsBlock` 返回空串）。
- [x] 路径遍历不越过文件系统根、不因权限错误 panic（静默跳过）。

**依赖：** 无。**风险：** 稳定区（前缀缓存）内容变化会改变请求字节；顺序必须确定（从远到近）。

**实施记录（2026-10-01，已完成）：**
- 改动文件：`context/agents.go`（重写加载逻辑）、`context/agents_test.go`（新增 6 个测试）、`cmd/run.go:322-324`（仅注释）。
- 落地决策（实施时确定，与原卡描述一致但更具体）：
  1. `LoadAgentsMD` **签名不变**，参数由 `projectRoot` 更名为 `cwd`（调用点 `cmd/run.go:324` 传 `os.Getwd()`，语义本就正确，无需改调用）。
  2. `Source` 标签改为**相对项目根**的路径（如 `a/b/AGENTS.md`），跨平台用 `filepath.ToSlash` 统一分隔符以保证注入字节稳定。
  3. `AGENTS.override.md` 规则：**非空才取代**同目录 `AGENTS.md`（空 override 不擦除约定）；用户级 `~/.deepact/` 目录同样适用该规则。
  4. 字节预算 `agentsMDMaxBytes = 32 * 1024`（对齐 Codex `project_doc_max_bytes` 默认值），**只作用于项目级**；超出时从离 cwd 最远者**整份丢弃**（不做字节级截断，避免把指令切在半句），**最近的一份始终保留**。
  5. 项目根探测用 `.git`（目录或文件均可）；向上走到文件系统根仍未命中 → 退化为只加载 cwd。
- 验证证据：`make test`（`go test ./... -count=1 -race -timeout=120s`）全绿，16 个包全部 ok，含 `context` 3.396s；`go build ./...` 通过。

---

### M2 — `apply_patch` 多 hunk 模糊补丁工具

**目标：** 提供一个"一次调用完成多处增/删/改/移动"的补丁工具，配合多级模糊定位，显著降低模型编辑失败率。

**Codex 证据：**
- `codex-rs/apply-patch/src/lib.rs:339`（`apply_patch` 入口）、`:470`（`apply_hunks_to_files` 逐 hunk 落盘）。
- `codex-rs/apply-patch/src/parser.rs:145`（`parse_patch`），`:6-22` 记录文本格式语法：`*** Begin Patch / *** Add File / *** Delete File / *** Update File / *** Move to / @@ / *** End of File / *** End Patch`。
- `codex-rs/apply-patch/src/seek_sequence.rs:12-25`：匹配逐级放宽——精确 → 忽略行尾空白 → 忽略首尾空白；`eof=true` 时先从文件尾尝试。
- 另有 Unicode 归一化（花式破折号/引号/NBSP → ASCII）在 `seek_sequence.rs` 后续实现中，实施前需重新确认。

**DeepAct 现状：**
- `tools/builtin/edit.go:26`：仅精确 `old_string` / `new_string` 替换。
- `tools/builtin/edit.go:142` `replaceFuzzy`：只对行尾做 `TrimRight` 后做整行匹配，无首部空白放宽、无 Unicode 归一化、无多 hunk、无移动/新建/删除文件。
- 注册方式参考 `cmd/run.go` 的 `registerBuiltinTools`（实施前定位）。

**落点：** 新增 `tools/builtin/apply_patch.go`（+ 测试），在 `cmd/run.go` 注册。

**验收标准：**
- [x] 支持 Add / Delete / Update / Move 四类 hunk，一次调用作用多文件。
- [x] 定位逐级放宽：精确 → 去行尾空白 → 去首尾空白；每级都有测试。
- [x] 匹配歧义（多处命中）时返回明确错误而非误改。
- [x] 返回值包含 git 风格的文件级摘要，可被 `ui/diff_viewer.go` 渲染。
- [x] 失败时**不产生部分写入**（原子性：先全量解析再落盘，或逐文件写回失败即回滚）。

**决策点 D2（已定，2026-10-01）：** 采纳**并存**——`edit` 保持不变，`apply_patch` 覆盖多文件/多 hunk；两者描述互相指路。

**风险：** 解析器边界（大文件、CRLF、二进制文件、路径越界）需显式拒绝。

**实施记录（2026-10-01，已完成）：**

*接口决策：改用 JSON 而非 Codex 的 `*** Begin Patch` 文本信封。* 理由：DeepSeek 的函数调用参数本就是 JSON（训练分布内），Codex 的文本信封是 OpenAI 自家训练的格式，对 DeepSeek 出错率更高；且 JSON 可直接复用 `edit` 已验证的替换语义，无需新增文本解析器。**这是对原卡"照搬解析器"的偏离，偏离理由记录在此。**

- 模型接口：`{"edits":[{op?, path, new_path?, old_string?, new_string?, content?, replace_all?}]}`；`op` 可省略，默认 `update`（枚举 update / add / delete / move）；`file_path` 兼容别名。
- 定位三级放宽：精确子串 → 行级忽略行尾空白 → 行级忽略首尾空白（复用 `findMatches` / `applyMatches`）；替换文本**原样插入、不做重新缩进**，与 `edit` 的 `replaceFuzzy` 语义一致。
- 原子性：阶段一全量解析+内存求值（任一失败 → 零落盘）；阶段二顺序写入，失败即逆序回滚，回滚失败会显式附在错误里而非吞掉。
- 结果契约（已对照 `ui.isDiffContent` / `ui.splitDiff` / `ui.parseDiffHunks` 核实）：第 1 行摘要 `applied N edit(s) across M file(s) (+X -Y)` → 连续多段 `--- a/…` 统一 diff → 末行 `backups: path=sha256:…`（无 artifact store 时省略）。备份 ref 可直接喂给 `revert`（有测试）。
- 接入点（全部落实）：`cmd/run.go:447` 注册；`engine/turn.go:694` 归入 destructiveCalls（顺序执行、逐个展示 diff）；`engine/turn.go:779` 计入 MadeProgress；`engine/turn.go` `updateTaskStateFromTools` 新增 apply_patch 分支（走 `engine/key.go` 新增的 `parseApplyPatchTargets`，因为该工具没有顶层 `path`，原 `extractPathFromArgs` 取不到）；`summarizeArgs` 新增分支；`ui/model.go` helpTools；`context/promptset/zh/system.md` 工具策略表；`tools/builtin/edit.go` 描述指路。
- 明确不改：`engine/sub_agent.go` 的只读宇宙（闭集，"未知即不允许"，确认无须改动）。
- 已知留白（**未做，非缺陷**）：`engine/key.go` 的 `repeatKeys` 未纳入 apply_patch，因此同一批次的重复调用不会收到 `[repeat N×]` 提示。原卡未列此项；如需与 `edit` 对齐，加一个基于 edits 数组哈希的 key 即可。
- 测试：`tools/builtin/apply_patch_test.go` 12 个用例（多文件多 hunk、顺序应用、三级定位、歧义拒绝、原子性、四类 op、错误信息、摘要契约、备份喂 revert、无 store 时不输出备份行）；`engine/apply_patch_integration_test.go` 3 个用例（路径提取、任务状态入账）；`summarizeargs_test.go` 新增 2 个表项并把 apply_patch 纳入 NeverEmpty 守卫。
- 验证证据：`make test`（`go test ./... -count=1 -race -timeout=120s`）16 个包全绿；`go build ./...`、`go vet` 通过。

**注意（前缀缓存）：** 本次改动同时改变了 tools 数组（新增一个工具）与 system prompt（工具策略表）——按 M1 排查的结论，这会让**下一次会话首轮**重新预热前缀一次，之后恢复稳定；命中率不会持续下降。

---

### M3 — 命令策略引擎 + 审批档位

**目标：** 把"危险命令判定"从硬编码字符串黑名单升级为**结构化规则 + 可持久化的批准 + 用户可选的审批档位**，让用户一次批准永久生效，减少重复打断。

**Codex 证据：**
- `codex-rs/execpolicy/src/decision.rs:9-16`：`Decision { Allow, Prompt, Forbidden }`。
- `codex-rs/execpolicy/src/policy.rs:28-32`：`Policy` 按程序名索引规则；`rule.rs:16-60` `PrefixPattern` / `PatternToken`（首 token 固定 + 备选）。
- `codex-rs/core/src/exec_policy.rs:216-238`：`AskForApproval` 四态（Never / OnRequest / UnlessTrusted / Granular），Granular 下 rules 审批与 sandbox 审批独立开关。
- `codex-rs/core/src/exec_policy.rs:357-392`：批准后可 auto-amendment，把 allow prefix 规则持久化，后续命令免询问。
- `codex-rs/core/src/safety.rs:18-23`：`SafetyCheck { AutoApprove, AskUser, Reject }`。

**DeepAct 现状：**
- `engine/guards.go:11-18`：`GuardAction` 四态 `GuardAllow / GuardBlock / GuardDiagnose / GuardAskUser`——**已有正确的结果契约，缺的是来源**。
- `engine/guards.go` 内危险命令判定（实施前定位 `checkDangerousBash`）。
- `tools/builtin/bash.go:30-45` `defaultBlocklist()`：硬编码字符串 → 说明，`isBlocked` 用 `strings.Contains`（脆弱代理，无结构化解析）。
- 缺：全局审批档位、规则文件、批准持久化。

**落点：** 新增 `engine/execpolicy/`（纯逻辑包：规则解析 + token 前缀匹配 + 决策），结果接入 `engine/guards.go` 的 `GuardAction`。

**验收标准：**
- [x] 规则以 Go 结构 + TOML/JSON 文件表达（**不引入脚本引擎**，Codex 用 Starlark，DeepAct 不复制）。
- [x] 规则按"程序名 + token 前缀模式"匹配命令，支持可选备选 token。
- [x] 决策映射：Allow → `GuardAllow`，Prompt → `GuardAskUser`，Forbidden → `GuardBlock`。
- [x] 用户批准后可将 allow 前缀规则写入用户级配置，下次免询问（需与安全红线对齐：**绝不写入凭证类信息**）。
- [x] 审批档位（如 `never` / `on-request`）作为全局配置生效。
- [x] **与现有 guards 判定融合，不产生两条并行判定路径**。

**决策点 D3（已定，2026-10-01）：** ① 档位放 `config.toml`（新增 `[approval]` 段）② 保留 `defaultBlocklist`，且它是不可绕过的最后防线。

**风险：** 规则引擎最容易过度设计（Codex 侧还有 network-rule、sandbox_migration 清理等）。**建议只做 prefix 规则**，其余不做。

**实施记录（2026-10-01，已完成）：**

*与卡面的一处偏离：* 落点从"新增 `engine/execpolicy/` 独立包"改为 **`engine/approval.go`（同包）**。理由：规则匹配必须复用 `danger.go` 里已有的 `unwrap` / `literalArgs` / `isShellInterpreter` / `shellCommandString`（否则要跨包导出内部辅助函数，或复制一份），同包是最小改动。

*第二处偏离（经批准的设计阶段确定）：* 卡面写"批准后写入用户级配置"，实际写入**独立文件 `~/.deepact/approvals.toml`**。原因：`config.LoadProject` 是"项目优先、不合并"（`config/config.go:145-162`），写进 `~/.deepact/config.toml` 会在任何带 `.deepact/config.toml` 的项目里被静默遮蔽。

- **判定链（安全契约，逐条测试）**：① `judgeDanger` 的 dangerSystem 硬拦，规则**永远无法放行** ② 规则 `forbidden` 硬拦（带规则 reason）③ 规则 `allow` + 已批准命令放行 ④ dangerProject 或规则 `prompt` 询问（`mode = "never"` 时放行）。`judgeDanger` 与 `defaultBlocklist` **一行未改**。
- **匹配语义**：复用 shell AST（`mvdan.cc/sh`），`syntax.Walk` 覆盖 pipeline / 列表 / 子 shell / 命令替换，并下降一层 `bash -c` payload（沿用 `maxNestedShellDepth`）；`unwrap` 剥离 `sudo`/`doas`/`command`/`env` 包装。**不做 flag 顺序归一化**（`-r -f` ≠ `-rf`），已在 `ApprovalRule` 注释中写明，并建议 forbid 规则留空 `args` 以覆盖全部调用形式。
- **(i)+(iii) 的落地**：确认后只记住**空白折叠后的那条精确命令**（`normalizeApprovalCommand`），跨会话生效；确认弹窗里追加一段 `config.toml` + `[[approval.rules]]` 的声明示例，把"放宽"保留为用户的显式动作。
- **档位容错**：未知/空 mode 一律回落 `on-request`（失败方向朝着"询问"），配置里的 `mode`/`decision`/`program`/`args` 非法值在 `Apply` 阶段 **fail loud** 并拒绝启动。
- 改动面：`engine/approval.go`（新建）、`engine/guards.go`（判定链 + `ConfirmDangerous` 回调 + `ScopeGuard` 三字段）、`engine/types.go`（`EngineConfig` 四字段）、`engine/loop.go`（`NewEngine` 装配）、`config/config.go`（`[approval]` 段 + `applyApproval` 校验 + `approvals.toml` 读写，0600 + tmp/rename）、`cmd/run.go`（读已批准 + 持久化回调，写失败只告警不阻断）、两份 README 的配置示例。
- 测试：`engine/approval_test.go` 18 个优先级表项 + 7 个匹配语义表项 + 学习/回调/文案 4 项；`config/approval_test.go` 段落解析、映射、默认不覆盖、5 类非法值 fail-loud、往返/去重/引号转义/0600 权限。
- 验证证据：`make test` 16 个包全绿；`go build ./...`、`go vet` 通过。
- 已知留白（**未做，非缺陷**）：仅对 `bash` 工具生效（`CheckTool` 只拦 bash，与既有行为一致）；`mode = "never"` 不影响子代理（子代理本就只读、无 bash）。

---

### M4 — Slash 命令注册表

**目标：** 把 UI 层的"命令字符串 → if 分支"升级为"命令 → handler 注册表"，使新增命令无需改核心分发逻辑。

**Codex 证据：**
- `codex-rs/tui/src/slash_command.rs:12-313`：`SlashCommand` 枚举，带描述、别名、可用性门控（`available_during_task` / `available_in_side_conversation`）。
- `codex-rs/tui/src/bottom_pane/slash_commands.rs:71-159`：`builtins_for_input` / `find_slash_command` 统一匹配。
- `codex-rs/tui/src/bottom_pane/command_popup.rs`：弹出式补全。

**DeepAct 现状（注意：不是从零建设）：**
- `ui/model.go:82-88` `slashCommands` 静态表：`/help`、`/clear`、`/ratd`、`/collab`、`/resume`。
- `ui/model.go:1310-1359` `updateSuggestions`：前缀过滤 + 补全弹窗（已含 skill 动态项）。
- `ui/model.go:1448-1470` `submitInput`：**仅 `/help` 与 `/resume`** 是硬编码本地分支；其余走引擎。

**落点：** `ui/`（建议新增 `ui/commands.go`）。

**验收标准：**
- [x] 定义 `slashCmd{ Name, Args, Desc string; Handle func(*Model, string) (tea.Model, tea.Cmd, bool) }` 形态的注册表。
- [x] 现有 5 条命令改为注册项，行为逐条等价（有测试或手动验证记录）。
- [x] skill 快捷命令（现 `ui/model.go:1338-1346`）作为动态注册项接入，而非特判。
- [x] 保持"本地处理不启动引擎 / 其余交给引擎"的既有语义不变。
- [x] 支持"运行中可用 / 不可用"门控字段（对应 Codex 的 `available_during_task`）。

**依赖：** 无。**风险：** `submitInput` 与 `engine.Run` 的启动耦合（`state→running`、spinner、`runSeq`）需要在 handler 形态下保持。

**实施记录（2026-10-01，已完成）：**

- **注册表**：新增 `ui/commands.go`。`Command{Name, Args, Description, Source, AvailableDuringRun, Handle}`；`Source`（builtin | skill）让 `/help` 仍分两节渲染；`Handle == nil` 表示**引擎负责**（`/clear`、`/ratd`、`/collab`、`/<skill>`），UI 只登记不截胡。`builtinCommands()` 用**函数**而非包级变量，因为 handler 会经 `allCommands()` 回读注册表，用 var 会构成初始化循环（编译期报错，已修）。
- **单一数据源**：`allCommands()` 同时喂三个消费者——建议弹窗（`updateSuggestions` 去掉了 skill 特判分支）、`/help`、`submitInput` 分发。`SetSkillSuggestions` 的对外签名未变，`cmd/run.go` 无需改动。
- **分发语义**：`localCommandFor(input, running)` 先纯查询再执行，调用方按需决定副作用。保真规则：`Args == ""` 的命令要求精确匹配，故 `/help me` 仍回落引擎；大小写不敏感。
- **两处调用点**：`submitInput`（空闲，替换原 `/help`、`/resume` 两个 if 分支）与 `ActionSubmit` 的运行中分支（替换原无条件 steer）。未处理时保留原路径：就绪 → `engine.Run`；运行中 → `EngineRunner.Steer` + `Queued` 气泡。原 `TestRunningStateEnterQueuesMessage` 未改仍绿，是"steer 语义未变"的证据。
- **行为变更（设计阶段已批准）**：`/help` 标记 `AvailableDuringRun = true`，运行中本地显示帮助，不再作为 steer 消息塞给模型。`/resume` 保持 false。
- **测试发现并修掉的真实缺陷**：`handleHelpCommand` 原先无条件 `m.state = stateReady`（照搬旧代码），在运行中执行会把状态从 `stateRunning` 拉回就绪——spinner 卡住、Esc 失效、再回车会启动第二个 Run。已改为仅在非运行中时归一化，并有 `TestRunningStateSlashHelpAnsweredLocally` 守住。
- 改动面：`ui/commands.go`（新建）、`ui/model.go`（删 `slashCommands` 变量；`updateSuggestions` 走注册表；`submitInput` 与运行中分支改为注册表分发；`buildHelpText(commands []Command, tools []Suggestion)` 按 `Source` 分节）、`ui/commands_test.go`（新建 12 项）、`ui/help_test.go`（3 项仅改签名，**断言逐字未动**——输出未变的证据）。
- 验证证据：`make test` 16 个包全绿；`go build ./...`、`go vet ./ui/...` 通过；新增文件 `gofmt` 干净。

---

### M5 — 模型 provider 抽象 + provider 表

**目标：** 把"DeepSeek 单供应商 + 类型断言"改为"接口 + 可序列化 provider 表 + 能力上限"，使接入其他 OpenAI 兼容后端成为配置而非改代码。

**Codex 证据：**
- `codex-rs/model-provider/src/provider.rs:141`：`trait ModelProvider`；`:369` `create_model_provider`；`:52-58` `ProviderCapabilities`。
- `codex-rs/model-provider-info/src/lib.rs:135`：可序列化 `ModelProviderInfo`；`:473` `api_key()` 从 `env_key` 解析；`:651-679` 内置 provider 表（openai / amazon-bedrock / ollama / lmstudio）。

**DeepAct 现状：**
- `llm/client.go:8`：`Client` 接口（`Stream` / `Complete`）——运行期抽象已有。
- `llm/adapter.go:74,89`：**`c.client.(*DeepSeekClient)` 类型断言**（改端点/换模型直接依赖具体类型）。
- `router/selector.go:22-23`：模型名 `deepseek-v4-flash` 硬编码。
- `config/config.go:83-87`：`modelConfig` 只有 `base_url` / `api_key` / `reasoning_effort`，无 provider 概念；`config.go:165-176` `LoadAPIKey` 走 `DEEPSEEK_API_KEY` env → TOML。

**落点：** `llm/`（provider 接口与表）+ `config/config.go`（配置段）+ `router/selector.go`（去硬编码）。

**验收标准：**
- [ ] 新增 `ProviderInfo`（name / base_url / env_key / 模型列表 / 能力标志），从 config 加载。
- [ ] 移除 `llm/adapter.go` 中的 `*DeepSeekClient` 类型断言，改由接口方法表达（如 `WithModel` / `WithBaseURL`）。
- [ ] 默认行为与现在完全一致（DeepSeek 为默认 provider，零配置可用）。
- [ ] 密钥解析顺序：env（`env_key` 指定）→ 用户配置，**不新增密钥落盘方式**。

**依赖：** 无。**风险：** 过度抽象。**不要**搬 Codex 的远程模型发现 + 磁盘缓存（`models-manager`）。

---

### M6 — `tool_search` / 动态工具发现

**目标：** 工具（尤其 MCP 工具）数量增长时，避免每轮把所有工具 schema 塞进 prompt；改为暴露精简目录 + 检索式按需加载。

**Codex 证据：**
- `codex-rs/tools/src/tool_discovery.rs:6`：`TOOL_SEARCH_TOOL_NAME`，默认返回 8 条。
- `codex-rs/tools/src/tool_search.rs:112`：`default_tool_search_text` 生成检索文本。
- `codex-rs/core/src/tools/handlers/tool_search.rs:159`：BM25 检索引擎；`:212` query 处理。
- `codex-rs/tools/src/dynamic_tool.rs:5`：`parse_dynamic_tool`，含 `defer_loading`。

**DeepAct 现状：**
- `tools/registry.go:175-185` `AllSpecs()`：返回全部工具并按 name 排序（注释明确是为 **DeepSeek 前缀缓存**保持字节稳定）。
- `tools/adapter.go:13-29`：把 registry specs 转成模型 tool spec 列表。
- 缺：延迟加载、按需发现、检索。

**落点：** `tools/registry.go`（deferred 工具集）+ 引擎侧新工具 `tool_search`（参考现有引擎内建工具的装配方式）。

**验收标准：**
- [ ] `registry` 支持标记工具为 deferred；`AllSpecs` 对 deferred 只返回名称 + 单行描述。
- [ ] 新增 `tool_search` 工具，按 query 返回最相关的 N 个完整 spec（N 为命名常量）。
- [ ] **默认不启用**（工具数低于阈值时行为与现状一致），启用后逐轮请求字节仍然稳定。
- [ ] 检索命中后该轮即可调用被发现的工具（写回 registry 或本轮 spec 追加）。

**依赖：** 无。**风险：** 与 `registry.go:180` 的排序稳定性注释冲突——任何动态追加都会改变 tools 数组字节，必须保证"发现结果在本轮内稳定且可复现"。

---

### M7 — 生命周期 hooks / notify 外部命令钩子

**目标：** 让用户/CI 在会话与工具生命周期节点挂外部脚本（通知、审计、自动提交），把能力扩展点交出去而不是写死逻辑。

**Codex 证据：**
- `codex-rs/hooks/src/lib.rs:23-36`：`HOOK_EVENT_NAMES` 共 12 类——`PreToolUse` / `PermissionRequest` / `PostToolUse` / `PreCompact` / `PostCompact` / `SessionStart` / `SessionEnd` / `UserPromptSubmit` / `SubagentStart` / `SubagentStop` / `Stop` / `Interrupt`。
- `codex-rs/hooks/src/legacy_notify.rs:29-73`：把 turn 完成事件以 JSON 追加到 argv 执行外部命令（最小可用切片）。
- `codex-rs/hooks/src/engine/`：dispatcher + 命令/MCP 两类执行器。

**DeepAct 现状：**
- `engine/stop_hook.go:32-49`：`StopHook` 接口 + `SetStopHooks`，注释明确"引擎无内置 hook，外部可注入"——**只有进程内接口，无外部命令式钩子**。
- 缺：事件命名规范、payload schema、`notify` 配置、超时/失败策略。

**落点：** `engine/`（事件总线，复用 `stop_hook.go` 的注册模式）+ `config/config.go`（`[[hooks]]` / `notify` 段）+ `ui/`（可选 `/hooks` 浏览）。

**验收标准：**
- [ ] 事件名常量表（建议**只落最小集**：`SessionStart` / `SessionEnd` / `UserPromptSubmit` / `Stop` / `PreToolUse` / `PostToolUse`）。
- [ ] payload 为稳定 JSON schema，事件通过外部命令执行，带**超时**与失败隔离（hook 失败不阻断主流程）。
- [ ] hook 环境变量中**不得注入 API key / 凭证**（安全红线）。
- [ ] 与既有 `StopHook` 接口关系明确（建议：外部 hook 为 StopHook 的一种实现，而非新建并行通路）。

**依赖：** 与 M3 协同（`PermissionRequest` 事件语义）。**风险：** 事件面铺太大；建议先落 `notify`（turn 完成回调）这一最小切片。

---

### M8 — git / 环境快照注入上下文

**目标：** 让模型每轮知道当前分支、HEAD、工作区是否脏，以便做出符合仓库状态的编辑与提交决策。

**Codex 证据：**
- `codex-rs/git-utils/src/info.rs`：`GitInfo { commit_hash, branch, repository_url }`。
- `codex-rs/core/src/realtime_context.rs:520-526`：把 `Latest branch` 注入上下文。

**DeepAct 现状：**
- `context/builder.go:158-170` `buildEnvironmentInfo`：已注入 OS / Arch / CWD / Date。
- `context/dirtree.go`：目录树快照。
- 缺：git 信息。

**落点：** `context/builder.go`（若 M10 已落地，则作为一个 fragment）。

**验收标准：**
- [ ] 注入当前分支、HEAD 短 hash；可选：是否有未提交改动。
- [ ] **非 git 目录静默降级**（参照 `context/agents.go:38-49` 的静默跳过模式），不报错、不改变输出结构。
- [ ] 会话级采集一次并缓存（避免每轮执行 git 子进程，且保持稳定区字节不变）。
- [ ] 不因 git 命令缺失/超时而阻塞会话。

**依赖：** 无。**风险：** 稳定区顺序——插入位置需固定，否则破坏前缀缓存。

---

### M9 — worktree 隔离工作区

**目标：** 让"写类子代理"在独立 git worktree 中工作，使多个方向并行改动互不污染。（这是 DeepAct 从"只读子代理"升级为"可写子代理并行"的前置。）

**Codex 证据：**
- `codex-rs/worktree/src/lib.rs:25-45`：`ManagedWorktree { root, cwd, source_root, source_cwd, head_sha, branch }`、`CreateWorktree`。
- `codex-rs/worktree/src/lib.rs:30-166`：创建/列举/绑定线程/释放；用 `git worktree add --detach --no-checkout` 建独立检出。
- `codex-rs/worktree/src/lib.rs:284-329`：remove 前**拒绝本地改动**以免误删。
- `codex-rs/worktree/src/metadata.rs:19-86`：以 `.git/codex-thread.json` 记录 worktree 归属线程。

**DeepAct 现状：**
- 全仓 grep `worktree` **无实现**（仅测试文件里的字符串）。
- 子代理当前只读；主代理直接改 cwd。

**落点：** `engine/worktree.go` 或独立 `internal/worktree`（封装 git 命令）。

**验收标准：**
- [ ] `create / list / bind / remove` 四个操作，纯 git 命令封装，无新依赖。
- [ ] remove 前检测本地改动并拒绝（除非显式 force）。
- [ ] 归属信息持久化（可供 agent 拓扑恢复使用）。
- [ ] 跨平台路径正确（macOS / Linux / Windows 路径分隔与大写盘符）。
- [ ] **默认关闭**：不影响现有单工作树流程。

**依赖：** 无，但设计成本高。**风险：** 孤儿 worktree 回收、detached HEAD 语义、与现有子代理生命周期（Run 退出即取消）的对齐。**建议本卡单独走一次设计评审再实施。**

---

### M10 — context-fragments marker 化注入

**目标：** 让"注入了什么、如何识别并回剥"由片段自身声明（结构化 marker），而不是靠硬编码显示文本匹配。

**Codex 证据：**
- `codex-rs/context-fragments/src/fragment.rs:64-134`：`ContextualUserFragment` trait——`role()` / `content_kind()` / `markers()` / `body()` / `render()`，`matches_marked_text` 用大小写不敏感的 start/end marker 识别注入内容。
- `codex-rs/context-fragments/src/additional_context.rs:1-102`：`AdditionalContextUserFragment`（role=user）、`AdditionalContextDeveloperFragment`（role=developer），value 截断到 1000 token。

**DeepAct 现状：**
- `context/builder.go:59` `InjectedBlocks()`：返回**具体字符串切片**。
- 引擎据此做**硬编码 header 剥离**以识别 echo（实施前定位调用点）。
- 违反规则："用显示文本/内容作为查找键（当结构化 ID 存在时）"。

**落点：** `context/`（引入轻量 `Fragment` 结构）+ 引擎侧剥离路径改造。

**验收标准：**
- [ ] `Fragment{ Role, Kind, StartMarker, EndMarker, Body }` + `Matches(text)`。
- [ ] 现有 Block S / AGENTS / skills 三类注入改为 fragment 产出。
- [ ] 引擎剥离改走 `Matches`，不再依赖具体文案。
- [ ] **fragment 渲染顺序确定**，前缀缓存字节稳定性有回归测试。
- [ ] 新增注入源（如 M8 的 git 快照、M13 的记忆）只需新增 fragment，不改剥离逻辑。

**依赖：** M1、M8（建议在其之后做，避免顺序反复变动）。**风险：** 触碰稳定区，需谨慎。

---

### M11 — MCP streamable-http transport

**目标：** 让 DeepAct 能连接远程 HTTP MCP server，而不只是本地 stdio 子进程。

**Codex 证据：**
- `codex-rs/rmcp-client/src/rmcp_client.rs:113-177`：`PendingTransport` / `TransportRecipe`——stdio / streamable-http / in-process 多形态。
- `codex-rs/codex-mcp/src/connection_manager.rs:1-6`：聚合多个 client 的工具与资源。

**DeepAct 现状：**
- `tools/mcp/transport.go:15-28`：`Transport` 接口 + 仅 `StdioTransport` 实现。
- `tools/mcp/{client,manager,config,tool}.go`：client 初始化、子进程管理、`.deepact/mcp.json` 配置、工具注册。
- 缺：HTTP transport、资源读取。

**落点：** `tools/mcp/`（新增 HTTP transport 实现同一 `Transport` 接口）。

**验收标准：**
- [ ] 新增 HTTP transport，实现既有 `Transport` 接口（不改 manager/client 契约）。
- [ ] 配置可声明 transport 类型与端点。
- [ ] 连接失败/超时有明确错误，不阻塞其它 MCP server 与本轮对话。
- [ ] **不引入 OAuth**（Codex 的 OAuth 依赖 keyring + 刷新调度，破坏轻量定位）。

**依赖：** 无。

---

### M12 — app-server 协议最小内核（stdio JSON-RPC）

**目标：** 把 DeepAct 暴露为可被 IDE/外部客户端驱动的服务（headless 之外的长驻通道）。

**Codex 证据：**
- `codex-rs/app-server-protocol/src/rpc.rs:37-56`：`JSONRPCMessage` / `JSONRPCRequest` 四态信封（注释：**不发 `jsonrpc:"2.0"` 字段**，是 Codex 方言）。
- `codex-rs/app-server-protocol/src/common.rs:212,499`：宏生成方法表与 `initialize` / `thread/start` / `turn/start` 等方法。
- `codex-rs/app-server-transport/src/transport/mod.rs:80-95`：transport 枚举与 `TransportEvent`。
- `codex-rs/app-server/src/lib.rs:790-830`：按 Stdio / UnixSocket / WebSocket 启动。
- **重要事实：Codex 并不把自己作为 MCP server 暴露**（生产代码无 `rmcp` RoleServer；`app-server-transport/src/outgoing_message.rs:27-29` 注释明确 "app server as opposed to an MCP server"）。

**DeepAct 现状：**
- 无 JSON-RPC server / websocket / daemon。
- `cmd/exec.go`：一次性 headless 输出。
- 已有 JSON-RPC **client** 经验：`tools/mcp/` 与 `tools/builtin/lsp_session.go:138`。

**落点：** 新增 `server/` 包，复用现有 `engine.Run` 会话模型。

**验收标准：**
- [ ] 信封 + method 路由 + 连接事件模型，`encoding/json` + `net` 可用，**零新依赖**。
- [ ] 先只做 **stdio 单客户端**；transport 抽象留接口但不实现 daemon / 远程控制 / 托管安装。
- [ ] 一个连接内的会话生命周期与服务端事件（tool 开始/结束、文本增量）语义明确。
- [ ] 与 CLI 交互模式互不干扰。

**依赖：** 无。**风险：** 这是一张**大卡**，需要先定协议边界（哪些方法进 v1）。建议先做设计文档再实施。**scope 警示：** 绝不搬 Codex 的 daemon / remote-control / managed install。

---

### M13 — 跨会话记忆自动提炼（单阶段起步）

**目标：** 会话结束时自动把本次会话提炼为跨会话记忆，而不是仅依赖手工/单轮快照。

**Codex 证据：**
- `codex-rs/memories/write/src/lib.rs:1-116`：**写路径两阶段**——stage1 从 rollout 抽取、stage2 consolidation；产物 `raw_memories.md` / `rollout_summaries/`；常量 `DEFAULT_ROLLOUT_TOKEN_LIMIT=150_000`、`CONTEXT_WINDOW_PERCENT=70`。
- `codex-rs/memories/read/src/lib.rs:1-16`：读路径（`memory_root = codex_home/memories`，citation 解析）。
- `codex-rs/state/src/model/memories.rs:8-68`：`Stage1Output`、租约/退避/去重。
- `codex-rs/memories/read/src/usage.rs:9-16`：记忆来源分类。

**DeepAct 现状：**
- `memory/store.go:47-124`：按 cwd hash 分目录，`memory.json` + `memory.md`，tmp + rename 原子写，`Load` / `Save` / `Clear`。
- `memory/render.go:13-61`：渲染。
- `engine/compressor.go`：已有摘要能力（`archiveMessage`、`ParseArchiveSummary` / `ArchiveSummary`）。
- 缺：自动提炼、去重、成本控制。

**落点：** `memory/`（复用 `engine/compressor.go` 的摘要结构与模型调用）。

**验收标准：**
- [ ] 会话结束时（或用户显式触发）调用 flash 模型把会话归纳为现有 `MemorySnapshot` 结构并合并写回。
- [ ] **只做单阶段**；不做 stage2 consolidation、不做租约、不引入 sqlite。
- [ ] 有去重/水位机制，避免同一会话重复提炼造成记忆膨胀。
- [ ] 提炼失败不影响会话收尾（静默降级）。
- [ ] 记忆内容可被用户清除（复用现有 `Clear`）。

**依赖：** 无。**风险：** 每次会话尾多一次模型调用（成本）；记忆污染。建议加"最小会话长度阈值"。

---

### M14 — build-info + 进程诊断快照

**目标：** 让 `deepact --version` 等位置输出版本/commit/目标平台，并提供无内容的进程状态快照用于排障。

**Codex 证据：**
- `codex-rs/build-info/src/lib.rs:27-34,108-122`：version / commit / target。
- `codex-rs/diagnostics/src/lib.rs:82-106`：进程内存/gauge 快照，**不含内容**，平台实现分离（`:108-192`）。

**DeepAct 现状：**
- `internal/log/log.go`：仅 `DEEPACT_LOG` 文件日志。
- `cmd/root.go`：实施前确认是否已有 version 输出。

**落点：** `cmd/`（`-ldflags -X` 注入）+ `internal/` 诊断小函数。

**验收标准：**
- [ ] `version` / `commit` / `target` 三个变量，`Makefile`（已存在）构建时注入，源码默认值可运行。
- [ ] 诊断输出**不含对话内容、不含路径以外隐私、不含 API key**。
- [ ] 零新依赖。

**依赖：** 无。

---

## 四、依赖与顺序

```
M1 ─┐
M8 ─┼─► M10（fragment 化，需注入源先稳定）
M4   │
M2   │
M3 ──┴─► M7（hooks 事件语义需与审批档位协同）
M5   │
M6   │
M9 ──► （后续"可写子代理并行"独立计划）
M11  │
M12  │
M13  │
M14  │
```

无依赖、可立即开始的卡：**M1、M2、M3、M4、M5、M6、M8、M11、M13、M14**。

---

## 五、明确不做的事（负面清单）

以下项经调研确认**不适合移植**，除非定位发生变化，否则不要开工。若后续要翻案，必须写出推翻理由。

| 不做的项 | Codex 位置 | 理由 |
|---|---|---|
| 平台沙箱实现 | `codex-rs/sandboxing/{seatbelt,landlock,windows}.rs`、`bwrap`、`mxc-sandbox`、`codex-linux-sandbox` helper | 强依赖 OS 原生 API + 外部可执行文件，违背"单二进制零运行时依赖"。**仅**可借鉴"按平台能力选后端、都不可用则降级"的抽象 |
| shell-escalation | `codex-rs/shell-escalation/`（exec wrapper + `CODEX_ESCALATE_SOCKET`） | 需先有沙箱，无沙箱场景不成立 |
| 网络代理 / MITM | `codex-rs/network-proxy/` | 完整代理超范围；仅 `policy.rs` 的域名 glob 匹配逻辑可小规模借鉴（用于 fetch 白名单） |
| code-mode | `codex-rs/tools/src/code_mode.rs`、`code-mode-runtime` | 依赖 V8，无法静态嵌入 |
| sqlite 系 | `codex-rs/thread-store/`、`codex-rs/state/`（sqlite + migrations） | 零外部依赖定位冲突 |
| 云端与账号体系 | `cloud-tasks`、`core-plugins` 市场、`connectors`、`cloud-config`、`chatgpt` OAuth、`aws-auth`、`workload-identity` | 与 DeepSeek 原生定位无交集 |
| 遥测外联 | `otel`、`analytics`、`feedback`（硬编码 Statsig / Sentry 端点） | 破坏零依赖 + 隐私；仅 `diagnostics` 的离线无内容快照可用（见 M14） |
| 系统密钥链 / 加密落盘 | `keyring-store`、`secrets` | 需第三方库；现有 `0600` 文件 + env 已够 |
| 隧道 / 实时 | `tcp-tunnel`（HTTP/3）、`websocket-client`、`voice-host`、`realtime-webrtc` | 重依赖或产品化卫星，非核心能力 |
| feature flag 注册表 / config-schema 导出 | `codex-rs/features/`、`codex-rs/config-schema/` | 与精简 TOML 配置定位冲突；Go 无 `schemars` 等价物，需手工维护 |
| MCP user-verification | `codex-rs/rmcp-client/src/user_verification.rs` | 绑定 MCP elicitation 扩展，DeepAct 无对应协议 |

---

## 六、证据索引

### Codex 侧（本计划引用）
- 策略/安全：`execpolicy/src/decision.rs:9-16`、`execpolicy/src/policy.rs:28-32`、`execpolicy/src/rule.rs:16-60`、`core/src/exec_policy.rs:216-238,357-392`、`core/src/safety.rs:18-23`、`sandboxing/src/denial.rs:13-72`、`process-hardening/src/lib.rs:12-100`、`core/src/guardian/decision.rs:45-132`
- 工具：`apply-patch/src/lib.rs:339,470`、`apply-patch/src/parser.rs:6-22,145`、`apply-patch/src/seek_sequence.rs:12-25`、`tools/src/tool_discovery.rs:6`、`tools/src/tool_search.rs:112`、`tools/src/dynamic_tool.rs:5`、`core/src/tools/handlers/tool_search.rs:159,212`、`file-search/src/lib.rs:54,106`、`core/src/tools/handlers/unified_exec/exec_command.rs:75`、`core/src/tools/handlers/view_image.rs:59,201`
- 上下文/记忆：`core/src/agents_md.rs:58-116,192-296`、`core/src/agents_md_manager.rs:65-144`、`context-fragments/src/fragment.rs:64-134`、`context-fragments/src/additional_context.rs:1-102`、`memories/write/src/lib.rs:1-116`、`memories/read/src/lib.rs:1-16`、`state/src/model/memories.rs:8-68`、`git-utils/src/info.rs`、`core/src/realtime_context.rs:520-526`
- 多智能体：`worktree/src/lib.rs:25-45,30-166,284-329`、`worktree/src/metadata.rs:19-86`、`agent-message-board-client/src/protocol.rs:68-96`、`agent-graph-store/src/store.rs:17-59`、`cloud-tasks-client/src/api.rs:136-176`
- 协议/集成：`app-server-protocol/src/rpc.rs:37-56`、`app-server-protocol/src/common.rs:212,499`、`app-server/src/lib.rs:790-830`、`app-server-transport/src/transport/mod.rs:80-95`、`rmcp-client/src/rmcp_client.rs:113-177`、`model-provider/src/provider.rs:52-58,141,369`、`model-provider-info/src/lib.rs:135,473,651-679`、`build-info/src/lib.rs:27-34`、`diagnostics/src/lib.rs:82-106`、`external-agent-migration/src/{source_cla.rs:20,source_cur.rs:19,model.rs:52-87}`
- TUI：`tui/src/slash_command.rs:12-313`、`tui/src/bottom_pane/slash_commands.rs:71-159`、`hooks/src/lib.rs:23-36`、`hooks/src/legacy_notify.rs:29-73`、`tui/src/transcript_view.rs`

### DeepAct 侧（本计划引用的现状）
- `engine/guards.go:11-18`、`engine/stop_hook.go:32-49`、`engine/compressor.go:31-60,217-229`
- `tools/builtin/bash.go:30-45`、`tools/builtin/edit.go:26,142-180`、`tools/registry.go:175-185`、`tools/adapter.go:13-29`、`tools/mcp/transport.go:15-28`
- `context/agents.go:19-61`、`context/builder.go:59,158-170`
- `ui/model.go:82-88,1310-1359,1448-1470`
- `llm/client.go:8`、`llm/adapter.go:74,89`、`router/selector.go:22-23`、`config/config.go:83-87,165-176`
- `memory/store.go:47-124`、`memory/render.go:13-61`

---

## 七、未验证假设与待确认项

1. **DeepSeek 是否支持视觉输入未知** —— 决定 `view_image` / 附件粘贴是否有价值。开工前需确认。
2. **M2 与现有 `edit` 工具的关系**（并存 vs 替换）未定，见决策点 D2。
3. **M3 审批档位的配置形态与 `defaultBlocklist` 去留**未定，见决策点 D3。
4. **Codex 侧行号来自只读调研，部分方向（guardian 内部、core-plugins、voice-host）未逐一精读**，相关结论仅基于入口文件结构；引用前请复核。
5. **M9（worktree）与 M12（app-server）应各自先出一份设计文档**，本卡只给出边界与验收口径，不足以直接实施。
6. **前缀缓存稳定性是本项目的高敏感区**（见 `tools/registry.go:180` 注释）：M1 / M6 / M8 / M10 都会改变注入顺序或工具数组字节，实施时必须验证请求字节跨轮稳定。
