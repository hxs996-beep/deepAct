# DeepAct 非通用性实现审计（调研报告）

> 状态：**调研已完成；B1 与 T1（类型化部分）已于 2026-09-26 实施完成并验证**（见 §6）。
> 日期：2026-09-26
> 目的：本文件是唯一事实来源。后续 agent 直接按编号取用，**不要重新分析**；只需按"复核状态"一列决定是否需要补验证，并注意 §6 中已落地的改动。
> **复核（2026-09-27）**：见 §7。§6 的 4 组修复代码均正确；但 §6.3 声称的"漏剥离已修复"经实测不成立（P1），另有 2 处文档硬错误（P2/P3）、1 处遗漏（P4）与 1 处未声明风险（P5）。

## 0. 调研方法与范围

- 6 路并行只读调研（引擎流程 / 工具层 / 上下文与技能 / LLM 适配 / 安全判定 / UI 与评估），主 agent 复核全部关键 file:line 并补做引擎流程方向。
- 已覆盖目录：`engine/`、`tools/`（含 `tools/builtin/`、`tools/mcp/`）、`context/`（含 `langpacks/`、`promptset/`）、`llm/`、`router/`、`memory/`、`skill/`、`session/`、`config/`、`ui/`、`cmd/`、`artifact/`。
- 未覆盖（本轮未检查）：`cmd/apikey.go`、`cmd/set.go` 细节、`session/rewind.go`、`.worktrees/`、`dist/`。

### 判据（来自 AGENTS.md 两条第一性原理）

1. **泛化性优先**：方案应覆盖一类问题，而不是枚举具体场景；出现场景枚举（针对具体 case 的规则/启发式/分支）是泛化不足的信号。
2. **能力优先**：能给模型能力（工具、通道、可自主访问的信息）就不写死判定逻辑；固定模式只在需要确定性保护（安全、数据破坏、不可逆）时使用。

### 非通用性信号分类（下文"类别"列引用）

| 编号 | 含义 |
|---|---|
| ① | 场景枚举式补丁（为一个具体 case 加规则/分支） |
| ② | 硬编码清单（关键词、前缀、路径、语言、provider、模型名） |
| ③ | 用显示文案/文本匹配当查找键或判定依据（结构化字段已存在时） |
| ④ | 启发式阈值/猜测代替可被验证的信号 |
| ⑤ | 只支持单一语言/单一格式/单一平台的能力 |
| ⑥ | 假设特定 provider/模型/终端行为的兼容分支 |

### 已确认的"好模式"（**不要误报为问题**）

- `engine/guards.go:22-85`：`LoopTracker` 统一计数核心（4 个 guard 泛化为 1 个结构 + 多实例）——泛化的正面范例。
- `engine/agent.go:560-580`：`formatHandoffResult` 用结构化 `FinishReason` 而非文本前缀判定。
- `context/langdetect.go:24-41`、`context/langpacks.go:21-30`：检测结果显式标注 "heuristic" 并给出触发文件，让模型可自行纠偏。
- `skill/skill.go:13`：`Keywords []string // Retained as metadata`——关键词匹配已按设计移除，改为纯语义匹配。
- `tools/builtin/pathutil.go:36-57`：`resolveSafePath` 有意不做 workspace 范围限制（注释已说明理由：多项目编辑是本职），范围保护交给 Guards 层。
- `engine/turn.go:473` 附近：完成信号由模型显式发起（`task_complete`），无关键词匹配。
- `engine/loop.go:150-188`：`bgTask` 状态机用 `bgStateRunning`/`bgStateAwaitingUser` 常量。

---

## 1. 真 bug：非通用性的直接后果（最高优先级）

### B1. 英文会话的 system prompt 永远是 `"(loading...)"`

> 状态：✅ **已修复（2026-09-26）**。修复方式：`context/builder.go:79` 去掉 `a.userLang != ""` 门槛 + 用户语言类型化（见 §6）。

- **类别**：③ + ⑤（语言用显示串表达，非中文即空）
- **证据**：
  - `context/builder.go:79` — 门槛 `if a.userLangSet && a.userLang != "" && a.systemPromptWithLang == ""`
  - `context/langdetect.go:123-149` — `classifyTextLanguage` 对纯拉丁文本 `return ""`；只有 CJK 返回 `"中文"`，首字符假名返回 `"日本語"`
  - `context/builder.go:59-62` — `prompt == ""` 时回退字面量 `"(loading...)"` 作为 system 消息
  - 全仓唯一写 `a.userLang` 的位置是 `builder.go:72`；唯一写 `a.systemPromptWithLang` 的是 `builder.go:82`
- **推理链**：英文用户 → `userLang == ""` → 82 行永不执行 → system 消息恒为 `"(loading...)"`。
- **反证（说明是缺陷不是设计）**：子代理路径无此门槛（`engine/sub_agent.go:1032-1044` 无条件拼 system prompt）；`README.md` 宣称支持英文会话。
- **修复方向**：门槛改为 `if a.userLangSet && a.systemPromptWithLang == ""`（去掉 `userLang != ""`），并让语言表达类型化（见 T1），避免"空字符串＝英文"的隐式约定。
- **复核状态**：✅ 已复核（读代码确认，未运行时验证）。

---

## 2. 结构性非通用（建议逐个讨论）

### T1 语言被当成"显示字符串"当类型键

> 状态：🟡 **类型化部分已完成（2026-09-26，见 §6）**。剩余的检测/覆盖问题仍未处理，见本节末尾"仍未处理"。

- **类别**：③ + ②
- **证据（8 处）**：
  - 产出：`context/langdetect.go:140-149` 返回 `"中文"` / `"日本語"` / `""`
  - 消费：`context/prompt.go:22`（`isZH := userLang == "中文"`）、`context/langpacks.go:25,70`、`engine/prompts_i18n.go:14-15`（`zhFromLang`）、`engine/sub_agent.go:1035`、`engine/handoff.go:29,133-135`、`engine/turn.go:584-586`、`engine/loop.go:358-363`、`engine/compressor.go:170,180,425,432`
- **类型声明佐证**：`context/langdetect.go:13` `type Language string`（项目语言有类型），但**用户语言是裸 `string`**。
- **失效场景**：
  - 新增用户语言（日语已能被识别）需要改 8+ 处；且下游只有"中文 / 非中文"两态，日语用户会拿到英文文案。
  - `zhFromLang` 的命名暗示"zh vs en"，实际语义是"中文 vs 非中文"，与 `"日本語"` 冲突。
  - 中文语言包只覆盖 2/7 语言：`context/langpacks/zh/` 仅 `generic.md` + `go.md`，而 `context/langpacks/` 有 `generic/go/java/python/rust/typescript.md`。
- **通用化方向**：定义用户语言枚举常量（如 `LangZH`/`LangJA`/`LangEN`）+ 一处 `isChinese(lang)` 映射；`langpacks` 由行为表驱动（缺哪个语言的 pack 用 generic 兜底并显式声明）。
- **复核状态**：✅ 已复核。
- **仍未处理（本次未改动，下次讨论）**：
  1. **中文语言包只覆盖 2/7 语言**：`context/langpacks/zh/` 仅 `generic.md` + `go.md`；`langPack()` 会对缺失的中文变体静默回退英文（`context/langpacks.go:66-79`），中文用户拿到英文语言包而不自知。
  2. **`classifyTextLanguage` 检测缺陷**：汉字判定优先于假名（`context/langdetect.go:140`），因此**含汉字的日文一律判为中文**（实测 `"テストが失敗する"` → `中文`）；假名分支只在纯假名文本时可达。测试已把该行为固化并标注为已知限制（`context/langdetect_test.go`）。
  3. **`latin` 计数是死变量**：`context/langdetect.go:128` 声明并自增，从未参与判定。
  4. **检测只看项目根目录**：`DetectLanguages` 仅探测根目录的 manifest（`context/langdetect.go:56-79`）；`packageHasTypeScript` 还用 `strings.Contains(data, "\"typescript\"")` 兜底（`:106`）。
  5. `engine.msgIsChinese`（`engine/loop.go:900`）与 `context.classifyTextLanguage` 是同一判据的两份实现，靠注释同步。

### T2 引擎按工具名硬编码工具语义（4 处，同一根因）

> 状态：✅ **已分类处置（2026-09-26，见 §6.4）**：1 处可证 bug 已最小修复（`MadeProgress` 漏算 `web_search`/`fetch`）；其余 4 处判定为"完善（不动）"。

- **类别**：① + ② + ③
- **证据**：
  | 位置 | 内容 |
  |---|---|
  | `engine/turn.go:708-765` | `MadeProgress` 判定：`case "edit","write","revert","bash"` 才算进展；read / read_multi / grep / glob / lsp 各写一套 key 构造（`extractReadScope`、`searchKey`、`lspKey`） |
  | `engine/turn.go:1074-1116` | `updateTaskStateFromTools`：`switch call.Name` 枚举 `read_multi` / `edit,write` / `read` / `grep,glob`；`:1111` 用裸串 `results[i].Status == "ok"`（`tools.StatusOK` 定义在 `tools/registry.go:43`） |
  | `engine/turn.go:810-1000` | `summarizeArgs`：按工具名分支（todo_write / plan_task / skill_install / load_skill / handoff_to_agent / grep / glob / read / read_multi / lsp）+ 猜参数键名（`command` / `operation` / `path` / `pattern` / `include` / `goal` / `agent`） |
  | `engine/turn.go:1118-1160` | `parseReadMultiDigestScopes`：从 result digest 正则解析 `<!-- read_multi targets: path::scope | ... -->` 注释还原结构化信息（生产者见 `tools/builtin/read_multi.go`） |
- **失效场景**：
  - 新增工具（`artifact`、`skill_install`、`agent_poll`…）自动游离在任务状态/进展判定/UI 摘要之外 → 例如新工具的写操作不计入 `MadeProgress`，可能触发 progressLoop 误判。
  - 工具改名要同时改 4 处。
  - `<!-- ... -->` 注释当数据通道：改文案即断链（`parseReadMultiDigestScopes` 返回 nil 时静默跳过记账，见 `turn.go:1123-1124` 注释自陈）。
  - UI 侧同源问题：`ui/model.go:429-434, 555-573, 2259-2276, 2357-2367, 2496, 2509, 2539, 2552` 按工具名特判渲染；`ui/model.go:46-52` `ToolNode` 无渲染元数据字段。
- **通用化方向**：`tools.ToolSpec` 增加声明式元数据（是否变更文件 / 是否推进任务 / 摘要渲染 / 渲染品类），引擎与 UI 只读元数据不判名字；`read_multi` 通过结构化字段而非注释回传 per-target 信息。
- **复核状态**：✅ 已复核（4 处代码 + UI grep 结果均确认）。

### T3 内部提示回显剥离清单与真实提示词脱节

> 状态：✅ **最小修复已完成（2026-09-26，见 §6.3）**。清单已删除，改为从"本轮真实注入的块"派生标题行；带阈值模糊匹配、archive/reasoning/流式过滤等完善项已按"bug 最小修、完善不做"显式砍掉。

- **类别**：②（清单漂移）+ ③
- **位置**：`engine/dsml.go:95-120`（`internalPromptBlockPrefixes` / `internalPromptExactHeaders`），消费于 `engine/dsml.go:122-172`（`isInternalPromptHeader` / `stripInternalPromptEcho`）。
- **活条目（有生产者，勿删）**：
  - `# Block S: Session Context` / `# Block S：会话上下文`（`context/prompt.go:25,34`）
  - `# Language Pack`（`context/builder.go:82`、`engine/sub_agent.go:1042`）
  - `## Environment` / `## 环境`（`context/prompt.go:26,35`）
  - ⚠️ 某子代理曾把 `# Language Pack` 误判为死条目，已推翻。
- **疑似死条目（全仓 .go/.md 未找到生产者）**：
  `[TASK REMINDER]`(:99)、`<TASK REMINDER>`(:100)、`</TASK REMINDER>`(:101)、`## Recent Actions`(:102)、`## Reminder on tool usage`(:103)、`## Task State`(:116)、`## 任务状态`(:117)、`Files already read`(:111)、`已读文件`(:112)。
  - `Files already read` / `已读文件` 的真实来源已更名：`tools/builtin/read.go:24,33-35`（`fileUnchangedStub` / `unchangedReadHint`）。
- **漏条目（真实注入但不在清单 → 回显不会被剥离）**：
  - `## 代码库结构` / `## Codebase`（`context/prompt.go:45,48`）
  - `## Project Conventions (AGENTS.md)`（`context/agents.go:54`）
  - `## Available Skills`（注入块标题见 `cmd/run.go:149`；`engine/loop.go:404-409` 的 `## 可用的 Skills` 是 `/skills` 命令输出，不进模型输入）
  - ~~`# Memory`、`## 关键发现`、`## 决策记录`、`## 待解决问题`、`## 假设`（`memory/render.go:15,36,43,46,57-58`）~~ —— **误报（2026-09-27 更正）**：`memory.RenderMarkdown` 全仓仅被 `memory/store.go:101` 调用，用途是把人类可读的 `memory.md` 写到磁盘，从不进入模型输入，故该类"漏剥离"不存在。
  - `context/promptset/zh/system.md` 的全部章节标题（`# 身份`、`# 核心规则（必须遵守）`、`# 安全红线`、`# 工具使用策略`、`# 代码质量规则`、`# DeepSeek 特定约束（关键）` 等，见该文件 `^#` 列表）
- **失效场景**：模型回显任一未列块 → 污染用户可见输出与写回历史（`engine/dsml.go:88-91` 注释自陈这是必须避免的）。
- **通用化方向**：块头单一事实来源——生产者与剥离器共用同一常量/生成函数，而不是两处手抄清单；或改为"剥离已注入块"的结构化标记（如不可见哨兵），而非匹配自然语言标题。
- **复核状态**：✅ 已复核（逐条 grep 生产者）。

### T4 能力只覆盖"被测过的那一种形态"

- **类别**：⑤
- **已复核项**：
  - **`read` / `read_multi` 的 `symbol` 仅支持 Go**：`tools/builtin/read.go:79`、`tools/builtin/read_multi.go:112` 均为 `payload.Symbol != "" && strings.HasSuffix(safePath, ".go")`，非 `.go` 时**静默退化为全文读取**。
  - 工具描述如实标注（`read.go:50`、`read_multi.go:28`：`Works only for .go files`），**但提示词无语言限定地宣传**：`context/promptset/zh/system.md:81`（"LSP workspaceSymbol → LSP hover/goToDefinition → read symbol=X → read offset/limit"）、`:88`（"读文件 / 特定区段 / 单个符号 | `read`（offset+limit / symbol=X）"）。
  - 影响面：项目自带 6 种语言检测（`context/langdetect.go:15-22`），模型在 TS/Python/Rust/Java 文件上会用 `symbol` → 得到全文 → 可能触发重读/重试循环。
- **未复核项（子代理声明，动手前需验证）**：
  | 项 | 位置 |
  |---|---|
  | `grep` 依赖外部 `rg`，fallback 语义分叉（`.gitignore` 行为、`include` 路径 glob 静默失败） | `tools/builtin/grep.go:73-88, 191-259` |
  | `fetch` 只处理 HTML/text，含简单 tag 剥离 fallback | `tools/builtin/fetch.go:141-175, 228, 274` |
  | `bash` Windows 非 UTF-8 只按 GBK 解码 | `tools/builtin/bash.go:214-230` |
  | 剪贴板命令表只覆盖 pbcopy/wl-copy/xclip，非 Windows 缺实现时静默成功 | `ui/selection.go:399-412`、`ui/clipboard_other.go:5-11` |
  | MCP 客户端丢弃非文本内容、不按 ID 关联响应、不处理 notification | `tools/mcp/client.go:105-113`、`tools/mcp/transport.go:78-93` |
  | `glob` 缺 `?`/`[]` 支持，与 `filepath.Match` 方言冲突 | `tools/builtin/glob.go:172-202` |
  | `skill_install` 硬编码 registry URL 与安装路径 | `tools/builtin/skill_install.go:19, 96, 102` |
  | `search.go` 的 `Provider` 为空操作，硬绑 Tavily | `tools/builtin/search.go:20, 46-48` |
  | LSP 语言服务器映射（有可配置 override，倾向判定为可接受） | `tools/builtin/lsp_server.go:39-64` |

### T5 安全判定：枚举本身合法，但有具体漏报与注释失真

- **类别**：②⑤⑥（**注意**：安全层是"确定性保护"的合法领地，不要因为它枚举命令就判为问题；本节只列客观缺陷）
- **好模式**：`engine/danger.go:56-144` 用 shell 结构解析（`syntax.Walk` + `judgeCall` + `judgeRm/judgeDd/judgeChmod/judgeGit`），优于纯字符串匹配。
- **已复核缺陷**：
  - `unwrap`（`engine/danger.go:~370-397` 区域）只剥离 `sudo/doas/env/nohup/command/builtin/time`，**无 `sh`/`bash` 处理**；`judgeCall` 的 switch（`:111-139`）也无 `sh`/`bash` case → `bash -c 'rm -rf /'` 判定放行。`judgeUnparsable`（`:86-100`）只在解析失败时兜底，对此无效。**结论来自读代码，未运行时验证。**
  - `tools/builtin/pathutil.go:74` 注释称 "Resolve symlinks/.." 但实际只调 `filepath.Clean`（不解析软链）；`:77` 为精确匹配集合（`/ /etc /usr /bin /sbin /var /System /Library`），`/etc/hosts` 不在列 → 软链/子路径可绕过。
  - 路径口径三处不一致：`engine/danger.go:338`（`/etc/` 前缀）、`tools/builtin/pathutil.go:70-85`（精确匹配）、`tools/registry.go:84-91`（`extractPath` 只认 `path` 键，`file_path` 别名绕过）。
  - 文件锁只覆盖 `edit`/`write` 且只认 `path` 键：`tools/registry.go:104-111` → `file_path` 别名与 `revert` 未被序列化（写竞争风险）。
- **未复核项（子代理声明，含具体样例）**：`xargs rm -rf` / `find -exec rm` 逃逸（`danger.go:372-397,129-132`）；`> file` 截断放行而 `:> file` 被拦（`danger.go:307-322,71-74`）；仅 `env -i`/`command -p` 逃逸已被证伪；SQL 客户端/语句枚举不全（`danger.go:135-138,544-553`）；Windows（cmd/PowerShell）破坏性命令全盲（`danger.go` 无 windows 分支；`bash.go:167`）；误报：`bash.go:150-163,31-45` 子串第二拦截层无确认，会误伤 `grep "dd if=/dev/"` 等（**误报项需优先验证**）；密钥不脱敏入模型上下文（`read.go:111,122`、`bash.go:125-131`，对比 `artifact/redact.go`）。

### T6 以文案/字节做数据解析

- **类别**：③ + ④
- **已复核**：
  - `engine/eval_store.go:293-318` `parseScoreFromText` 用 `Contains("Total Score:")` / `Contains("PASS")` / `Contains("FAIL")` 解析评分 → **全仓无调用者（死代码）**，仅定义处出现。
  - `"main_agent"` 裸字面量两处：`cmd/eval.go:157`、`engine/loop.go:867`（后者是写入方，`loop.go:833` 注释说明语义）。
  - UI 按工具名特判渲染：`ui/model.go:429-434, 555-573, 2259-2276, 2357-2367, 2496, 2509, 2539, 2552`（根因同 T2）。
- **未复核（子代理声明）**：
  - `ui/model.go:1654-1718, 1746-1747, 1766` 文本包含式判定。
  - 字节截断中文：`ui/model.go:514, 534, 2416, 2456, 2709, 2719`；`cmd/eval.go:113, 117, 125, 274`。
  - `engine/eval_store.go:273-274` JSONL 坏行静默丢弃。
  - `cmd/eval.go` 与 `engine/loop.go` 跨包裸字面量同族问题。

### T7 命令解析（判定为可接受，仅需小清理）

- **位置**：`engine/loop.go:916-996`（`parseSkillCommand`、`isClearCommand`、`parseConfirmCommand`、`extractTaskTextAfterSkillCmd`、`isValidSkillName`）、`engine/loop.go:752-755`（`isConfirmCommand`）。
- **判定**：CLI 命令语法必须是确定性的，属合法的固定模式，**不建议泛化**。
- **可清理项**：保留名 `clear`/`confirm` 在 `loop.go:932` 与 `parseSkillCommand` default 分支两处维护；`isValidSkillName` 写死长度 30 与字符集（`loop.go:952-962`）；`extractTaskTextAfterSkillCmd` 用前缀比较做参数切分。
- **复核状态**：✅ 已复核。

### T8 阈值类魔法数（仅风格，建议命名常量或不动）

| 常量 | 位置 |
|---|---|
| `maxSegmentRunes = 60` | `engine/turn.go:18-23`（用于 `:267`） |
| `iter >= maxIterations-2`（预算尾段收尾） | `engine/sub_agent.go:467` |
| `spent >= budget*80/100` | `engine/sub_agent.go:591` |
| `defaultMaxSuspendedSubAgents = 4` | `engine/loop.go:159-162` |
| `maxReadTokens=25000` / `charsPerToken=4` / `maxReadBytes=1MB` | `tools/builtin/read.go:19-22` |
| `len/4` 估算 | `engine/sub_agent.go:1149-1164`、`llm/token.go:16-18` |
| `subAgentToolResultCap = 512` | `engine/sub_agent.go:1238` |
| `readMultiMaxTargets = 8` | `tools/builtin/read_multi.go:12` |
| retry/limiter 常量 | `llm/retry.go:24-32`、`llm/limiter.go:102-141`、`router/selector.go:6-11` |

---

## 3. 横向规律（比单点更重要）

1. **结构化信息被降级成字符串再解析回来**：`read_multi` 的 HTML 注释（T2）、eval 评分文案（T6）、语言显示串（T1）。→ 原则：能传结构化字段就不传文案。
2. **用清单代替规则**：危险命令名（T5）、工具名（T2/T6）、语言清单（T1）、提示块头（T3）。此类清单**不会报错，只会静默失效**——T3 就是漂移已实际发生的现场。
3. **同一事实多处维护**：`userLang == "中文"` 8 处（T1）、`Status == "ok"` 2 处而 `tools.StatusOK` 存在（T2）、`"main_agent"` 2 处（T6）、保留命令名 2 处（T7）。
4. **能力只覆盖被测过的一种形态**：symbol 只 Go（T4）、fetch 只 HTML、剪贴板三命令、GBK 单一解码。

---

## 4. 推荐处置顺序（含执行状态）

1. **B1 + T1（类型化）** — ✅ **已完成并验证（2026-09-26，见 §6）**
2. **T3**（清单腐化） — ✅ **最小修复已完成并验证（2026-09-26，见 §6.3）**
3. **T2**（引擎按工具名硬编码） — ✅ **已分类处置（2026-09-26，见 §6.4）**：1 处 bug 最小修复，4 处判定"完善不动"
4. **T1 剩余部分**（语言包覆盖、检测缺陷） — 判定为"完善/内容工作"，**按原则不动**（见 §6.4 末尾分类表）
5. **T4**（先补验证未复核项，再决定哪些是能力缺口、哪些是设计取舍）
6. **T5**（先补验证误报/漏报样例，安全层改动需格外保守）
7. **T6 / T7 / T8**（清理与命名，低风险）

**用户决策状态：已完成 B1+T1（类型化）、T3（最小修复）、T2（分类处置 + 1 处 bug 修复）。下一项候选：T4 / T5 / T6（均需先补验证）。**

**用户确立的范围原则（适用于后续所有项）**：**是 bug 就最小方案修，是完善（可有可无）就不动。** T3 即按此执行：只修"漏剥离 / 误删内容"两个可证缺陷，砍掉一切可选增强。

---

## 5. 结论可靠度声明（后续 agent 必读）

- ✅ **已复核**：B1、T1、T2（含 UI grep）、T3（逐条生产者核对，并推翻子代理一处误报）、T4 的 symbol 项、T5 的注释失真/路径口径/文件锁项、T6 的 `parseScoreFromText` 死代码项、T7、T8 位置。
- ⚠️ **未复核，动手前必须再验证**：T4 表格中"未复核项"全部、T5 的逃逸与误报样例、T6 的字节截断行号与 UI 文本判定、以及子代理声称的 `llm/token.go:33-50` 校准口径不一致、`llm/deepseek.go:604-615` 仅 402 当 fatal、`router/selector.go:19-25` pro==flash 空转、`engine/key.go:35-61,152-177` 工具名/字段名 switch 丢键。
- 🚫 **判定为不必改**：见"已确认的好模式"一节，以及 T7（CLI 命令语法）、T8（阈值常量）。
- 本文件中的所有行号基于 2026-09-26 的工作区快照，改动后需重新定位。

---

## 6. 已实施变更记录（2026-09-26）

### 6.1 B1 修复 + T1 类型化：用户语言从裸字符串改为枚举类型

**核心设计**：类型 `engine.UserLanguage` 定义在 `engine` 包（`context` 与 `tools` 都单向依赖 `engine`，无 import cycle）。

| 项 | 内容 |
|---|---|
| 新类型与常量 | `engine/prompts_i18n.go`：`UserLanguage` + `LangUnset("")` / `LangChinese("中文")` / `LangEnglish("English")` / `LangJapanese("日本語")` + `IsChinese()` + `UserLanguageFor(zh bool)`；`zhFromLang` 改为接收类型化参数 |
| B1 门槛修复 | `context/builder.go:79`：`if a.userLangSet && a.systemPromptWithLang == ""`（去掉 `a.userLang != ""`） |
| 产出侧 | `context/langdetect.go`：`classifyTextLanguage`/`detectUserLanguage` 返回 `engine.UserLanguage`；`""` → `engine.LangEnglish`（Latin）/`engine.LangUnset`（无用户消息，不可达分支） |
| context 消费侧 | `context/prompt.go`、`context/langpacks.go`：参数类型化 + `IsChinese()` |
| engine 消费侧 | `engine/types.go`(ToolExecContext.UserLang)、`engine/agent.go`(Handoff.UserLanguage)、`engine/handoff.go`(handoffOptions/RunSubAgent×2/dispatchAsync)、`engine/sub_agent.go`(stableSystemPrompt/filterTools)、`engine/compressor.go`(字段+SetUserLang)、`engine/interfaces.go`(Compressor)、`engine/loop.go`、`engine/turn.go`：`UserLanguageFor(e.isChinese)` 取代 `""/"中文"` 派生 |
| tools/cmd 边界 | `tools/registry.go`(ToolContext.UserLang)、`tools/subagent.go`(SubAgentBackend 签名)、`cmd/run.go`(两个 backend 闭包 + `SetLangPacks(... LangChinese, LangEnglish)`) |
| 测试 | 新增 `context/builder_test.go::TestBuild_EnglishSessionGetsSystemPrompt`（英文会话必须拿到真实 system prompt，非 `"(loading...)"`）、`TestBuild_SystemPromptDeferredUntilFirstUserMessage`（占位符仅限首条用户消息之前）；新增 `context/langdetect_test.go`（类型化检测结果 + 首条消息锁定 + `UserLanguageFor`）；适配 `tools/subagent_test.go`、`tools/adapter_test.go` 的类型 |

**验证证据（实际运行输出）**：
- `go build ./...` → 成功（无输出）
- `go vet ./...` → 无告警
- `go test ./...` → `go test exit=0`，15 个包 ok，0 FAIL
- `go test -race ./engine/... ./context/... ./tools/...` → exit 0，无 DATA RACE
- `gofmt -l` 于本次改动文件：仅 `engine/types.go`/`engine/agent.go`/`engine/compressor.go` 被列出，且三者在 HEAD 版本就已未格式化（非本次引入）

**行为等价性核对**：中文路径完全未变；英文路径由 `zhFromLang("")`(false) 变为 `zhFromLang("English")`(false)，所有本地化分支取值不变，**唯一行为差异**是 system prompt 现在会被真正构建（即 B1 修复）。

**附带发现（未改动，已记录在 T1"仍未处理"）**：`context/builder.go` 在首次 Build 时先追加 system 消息再检测语言，因此首轮"预热 Build"（engine 用于 token 估算、其结果被丢弃）仍会返回占位符；引擎实际发送的是同一轮内的第二次 Build，不受影响。

### 6.2 工作区无关改动提示（**非本次引入，勿误认为本报告的一部分**）

执行前工作区已存在未提交改动：`go.mod` 删除了 `replace github.com/charmbracelet/bubbletea => ./third_party/bubbletea`、`go.sum` 增加 upstream bubbletea 校验和、`third_party/bubbletea/**` 处于已暂存删除状态。本次改动未触碰这些文件。

### 6.3 T3 最小修复：提示块回显剥离改为"从真实注入块派生"（2026-09-26）

> ⚠️ **表述限定（2026-09-27 复核后更正）**：本条修复的是**标题识别**——此前这些块的标题行完全不被清单承认。**块正文的回显仍会漏出**：剥离语义是"命中标题行后删到下一个空行"，而 Block S 的 `## 代码库结构`（标题 + 1 行描述 + 空行 + 围栏/目录树，`context/prompt.go:45-57`）与注入的 `## Available Skills`（标题 + 介绍段 + 空行 + 列表，`cmd/run.go:149-150`）都属于"标题后紧跟空行"的形态 → 只会删掉标题（及紧邻的介绍行），目录树 / 技能列表 / 系统提示词正文照旧漏给用户。实测（`stripInternalPromptEcho`，探针未落盘）：Block S 回显后仍残留 `"```\n…目录树…\n```"`；skills 回显后仍残留列表。此为既有限制，见本节末尾"已知缺口"。

**判定的 bug（仅这两条，其余一律不动）**
1. **漏剥离（标题识别层面）**：手写清单漏掉 4 类真实注入块的标题（`## 代码库结构`/`## Codebase`、`## Project Conventions (AGENTS.md)`、`## Available Skills`（`cmd/run.go:149`）、promptset 系统提示词章节标题），这些块的标题行此前完全不被识别。（本节原列的 `# Memory` 系列经 2026-09-27 复核为**误报**：`memory/render.go` 的输出只写 `memory.md`，不进模型输入。）
2. **误删内容**：清单中已无生产者的死条目（`## Task State`、`## 任务状态`、`Files already read`、`已读文件`、`[TASK REMINDER]` 系列、`## Recent Actions`、`## Reminder on tool usage`）会让**模型在自己答案里写同名小标题时被整段删除**。

**改法**：清单不再手写，改为**从本轮真实注入的块派生标题行**，规则=「整行相等」且该行结构化（ATX 标题 1–6 个 `#` + 空格；或 `[...]` 独立标记）。命中后仍按现有语义"删到下一个空行"。

| 文件 | 改动 |
|---|---|
| `context/builder.go` | +`InjectedBlocks()`：返回已缓存的 system prompt / Block S+AGENTS / skills 三块（改提示词即自动同步，不会再腐化） |
| `engine/interfaces.go` | `ContextBuilder` +`InjectedBlocks() []string`（编译期保证不漏实现） |
| `engine/turn.go` | 注入点收集 pinned（`turn.go:80-86`）→ 剥离点传入（`turn.go:343`） |
| `engine/dsml.go` | 删除两份手写清单与 `isInternalPromptHeader`；+`internalEchoHeaders` / `isMarkdownHeading` / `isBracketedMarker`；`stripInternalPromptEcho(content, injectedBlocks)` |
| `engine/dsml_internal_echo_test.go` | 重写为注入块驱动：覆盖漏剥离（代码库结构/AGENTS）、误删回归（死条目标题保留）、整行相等（相似标题保留）、纯回显→空、派生规则判定 |
| 2 个测试桩 | +`InjectedBlocks()`（`turn_load_skill_test.go`、`steer_test.go`） |

**显式砍掉的完善项（"可有可无的不动"）**
- 带阈值的模糊/连续行段匹配（覆盖"改写式回显"）——历史 spec 提到的该场景针对已不存在的 read_history 块，当前无证据。
- `[SESSION ARCHIVE]`、tool 结果提示、`reasoning` 的剥离。
- 流式 narration 的即时过滤（现状与 DSML 同为后置剥离，UI 已在流式期显示原文）。
- 单行 pinned（`[Background jobs] ...`）的剥离。

**已知缺口（写入测试注释固化，非本次修复目标）**
- 标题后紧跟空行的块只清掉标题（及紧邻的介绍行），正文保留 —— 现有"删到空行"语义的固有结果，也是本节顶部"表述限定"的实测依据。真实形态：`## Available Skills` + 介绍段 + 空行 + 列表（`cmd/run.go:149-150`）；Block S 的 `## 代码库结构` + 描述 + 空行 + 围栏目录树（`context/prompt.go:45-57`）。测试 fixture（`engine/dsml_internal_echo_test.go:9,12`）目前用的是简化形态与中文标题，且其注释（`:5-7`）把 skills 生产者记为 `engine/loop.go`——与真实生产者不一致，故该缺口在测试里只体现为"标题被删、正文保留"，看不出正文规模（改造 fixture 未在本次范围内）。
- 单行 pinned 消息不是独立 `[...]` 标记，不参与匹配。

**验证证据（实际运行输出）**：`go build ./...` 成功；`go vet ./...` 无告警；`go test ./...` → `exit=0`，15 包 ok、0 FAIL；`go test -race ./engine/...` → exit 0；改动文件 `gofmt -l` 干净。

### 6.4 T2 分类处置：只修 1 处可证 bug，其余判定"完善不动"（2026-09-26）

**判据**：该守卫的治理 spec `docs/superpowers/specs/2026-09-11-remove-heuristics-unify-loop-guards-design.md:126` 明文定义「**获取新信息（read 或 grep/glob）＝推进理解＝进展**」，并据此把 grep/glob 纳入 `MadeProgress`。

| 站点 | 判定 | 依据（可证的才修） |
|---|---|---|
| `MadeProgress` 漏算 `web_search`/`fetch`（`engine/turn.go`） | **bug → 已修** | 与 spec 声明的语义不一致：联网检索/取页同为"获取新信息"却未计数 → **连续 6 轮纯联网调用会被 progress guard（4 nudge / 6 block）误终止 Run** |
| `updateTaskStateFromTools` 未记 `revert`（`turn.go:1097-1103`） | 完善，不动 | 证不出用户可见损害：revert 的 ref 来自本会话 edit 的工具结果，该文件通常已在 `ModifiedFiles`；仅跨会话 resume 的窄场景可能漏 |
| `summarizeArgs` 按工具名/参数键猜显示（`turn.go:810-1000`） | 完善，不动 | 纯 UI 摘要降级（未识别工具显示裸名），无功能损害 |
| `read_multi` 用 `<!-- read_multi targets: ... -->` 注释回传结构（`read_multi.go:77-83` ↔ `turn.go:1125-1160`） | 完善，不动 | **实际比对：格式当前完全匹配、未失效**，只是脆弱耦合；改结构化通道＝重构（增强） |
| `Status == "ok"` 裸字面量（`turn.go:721,1111` ↔ `tools.StatusOK`） | 完善，不动 | 值相同，纯风格 |

**最小修复内容**

| 文件 | 改动 |
|---|---|
| `engine/key.go` | +`infoQueryKey(call)`：key = `web_search:<query>` / `fetch:<url>`，无 query/url 返回空 |
| `engine/turn.go` | `MadeProgress` 增 `case "web_search", "fetch"`，与 lsp/grep/glob 完全同机制（novel key 才置位） |
| `engine/progress_loop_test.go` | +2 用例：`TestExecuteTurn_MadeProgress_NovelWebSearch`（新 query → true）、`..._RepeatedFetch`（同 url → false） |

**未纳入（相邻完善项）**：`repeatKeys` 未包含 `web_search`/`fetch`（重复调用不会有 repeat 计数注释）——属另立的增强，本次不动。

**验证证据（实际运行输出）**：`go build ./...` 成功；`go vet ./...` 无告警；`go test ./engine/... -run MadeProgress -v` → 10/10 PASS（含 2 个新用例）；`go test ./...` → `exit=0`，15 包 ok、0 FAIL；`go test -race ./engine/...` → exit 0；改动文件 `gofmt -l` 干净。

**T1 剩余部分的分类（按同一原则：完善/内容工作，不动）**

| 项 | 判定 |
|---|---|
| `context/langpacks/zh/` 只有 generic+go（缺 java/python/rust/typescript 中文包） | 内容工作，非 bug（英文包仍有效规则）；不做 |
| `classifyTextLanguage` 汉字优先于假名（含汉字日文判为中文） | 启发式局限；且 system prompt 只有中文一套（`promptset.Get()`），修判定也不改变实际行为；不做 |
| `langdetect.go` 的 `latin` 计数死变量 | 死代码清理，无行为影响；不做 |
| `DetectLanguages` 只扫根目录；`packageHasTypeScript` 的字符串兜底 | 启发式局限，已在提示词里显式声明可纠偏；不做 |
| `engine.msgIsChinese` 与 `context.classifyTextLanguage` 两份实现 | 重复实现（一致性风险），无当前缺陷；不做 |

### 6.5 T5 子项①（安全漏报）修复：`sh -c '<cmd>'` 逃逸（2026-09-26）

**判定的 bug（代码路径静态确认，随后由回归测试固化）**：`bash -c "rm -rf /"` 完全绕过危险判定。
- `unwrap`（`engine/danger.go`）只剥离 `sudo/doas/env/nohup/command/builtin/time`，**无 `sh`/`bash`**；
- `judgeCall` 的 `switch base`（原 `:111-139`）也没有 shell 解释器 case → 落到"无判定 → 放行"；
- 与 `judgeDanger` 文档声明「judges … across **every command it would execute**」不符 —— `sh -c` 的 payload 正是它要执行的命令。

**最小修复**（方案 A：递归判定，保持该文件"结构化判定"风格）
| 文件 | 改动 |
|---|---|
| `engine/danger.go` | ① `judgeDanger` 保留为入口，新增 `judgeDangerDepth(cmd, depth)` 承载嵌套深度；② `judgeCall(call, depth)` 增 `case "sh","bash","zsh","dash","ksh","ash","fish"`：取 `-c` 后的字面量 payload 交回 `judgeDangerDepth(payload, depth+1)`；③ +`shellCommandString(args)`；④ `const maxNestedShellDepth = 1` |
| `engine/danger_test.go`（新增） | 19 个表驱动用例（2026-09-27 补充修复后增至 32 个，见 §6.6）：外层基线（`rm -rf /`、`curl … \| sh`、`mkfs.*`）、payload 等价（`bash -c`/`sh -c`/`sudo bash -c`/`env VAR=1 bash -c`/`fish -c`/带前置 flag）、无误报（`bash -c "ls -la"`、`bash -c "echo rm -rf /"`、`grep "drop table"`） |

**已知缺口（测试中固化，非本次目标）**：① `bash -c "$CMD"` 等非字面量 payload 不可判；② `bash script.sh`（脚本文件内容不可见）；③ 嵌套超过 `maxNestedShellDepth=1`（`bash -c "bash -c '…'"`）；④ Windows（cmd/PowerShell）破坏性命令仍不在 POSIX 解析范围内；⑤ `xargs rm -rf` / `find -exec rm` 未判。

**验证证据（实际运行输出）**：`go build ./...` 成功；`go vet ./...` 无告警；`go test ./engine/... -run TestJudgeDanger -v` → 19/19 PASS（当时快照；补充修复后为 32/32，见 §6.6）；`go test ./...` → `exit=0`，15 包 ok、0 FAIL；`go test -race ./engine/...` → exit 0；`gofmt -l` 干净。

**效果链**：`checkDangerousBash`（`engine/guards.go`）将 `dangerSystem` 映射为 `GuardBlock`（硬阻断）、`dangerProject` 映射为 `GuardAskUser`（需用户确认）→ `bash -c "rm -rf /"` 现在被硬阻断。

**T5 剩余候选的处置（2026-09-26，用户决策）**：② `xargs rm -rf` / `find -exec rm`、③ Windows（cmd/PowerShell）破坏性命令全盲、④ `tools/builtin/bash.go` 子串第二拦截层误报 —— **用户决定不再继续取证/修改**，三项保持"未验证"。

> ④ 的现场复现（意外获得，供后续参考）：写文档时用 `cat >> … <<'EOF'` 追加一段**文本**，其中含字面量 `dd if=/dev/`，该命令被工具层拦截（`destructive: raw disk write - blocked by policy`）——即纯文本中的危险子串触发误报，且该层无用户确认路径。此观察坐实 ④ 的误报描述，但按用户决策本次不处理。

### 6.6 T5 补充修复：shell 包装命令与组合短选项（2026-09-27，用户批准的最小修复）

**背景**：§6.5 只覆盖"参数中恰有 `-c`"的形态。复核用 `judgeDanger` 实测发现同类逃逸仍在（同一 bug 类的未闭合部分，属可复现缺陷）：

| 命令 | 修复前 | 修复后 |
|---|---|---|
| `bash -lc "rm -rf /"` / `bash -ec …` / `bash -lxc …` | `none`（放行） | `system`（硬阻断） |
| `sh -lc 'rm -rf /tmp/x'` | `none` | `project`（需用户确认） |
| `env -i bash -c "rm -rf /"` / `env -u FOO bash -c …` | `none` | `system` |
| `command -p bash -c "rm -rf /"` | `none` | `system` |
| `command -v rm` / `env -i printenv PATH` / `env FOO=1 ls` | `none` | `none`（无误报） |
| `bash -c` / `--noprofile -c` / `-o pipefail -c` / `sudo -n bash -c` / `nohup bash -c` / `dash -c` | 已判 | 仍判（无回归） |

注：§6.5 的原测试用例名 `bash with leading flag` 覆盖的是 `--noprofile -c`，**不含组合短选项**，这是本次补充修复的直接动机。

**改动**

| 文件 | 改动 |
|---|---|
| `engine/danger.go` | +`isShellCommandFlag(a)`：`-c` 本身，或**含 `c` 的短选项簇**（`-lc`/`-ec`/`-lxc`）；**显式排除长选项**——`--norc` / `--noprofile` 也含 `c`，若当作 `-c` 会吞掉真正的 payload（已用 `bash --norc -c "rm -rf /"` 用例钉住）。`shellCommandString` 改用它 |
| `engine/danger.go` | `unwrap` 的 `env`/`nohup`/`command`/`builtin`/`time` 分支不再把包装命令自身的第一个 flag 当成命令名：先跳过其选项（+`wrapperFlagTakesValue(wrapper, flag)`：`env -u/--unset/-C/--chdir/-S/--split-string`、`time -o/--output/-f/--format` 为取值选项，连带消费其值），再跳过 `VAR=value`，之后才是真实命令 |
| `engine/danger_test.go` | 用例 **19 → 32**：新增 8 个正例（`bash -lc`、`bash -ec`、`sh -lc`、`bash -o pipefail -lc`、`bash --norc -c`、`env -i bash -c`、`env -u FOO bash -c`、`command -p bash -c`）与 5 个负例（`bash -lc "ls -la"`、`bash --norc -c "ls -la"`、`env -i printenv PATH`、`command -v rm`、`env FOO=1 ls`） |

**影响面（超出"仅 shell payload"）**：`env`/`nohup`/`command`/`builtin`/`time` **所有**包装命令的判定口径都改变（方向：更严）。这是本次补充修复相对 §6.5"最小修复"叙述的越界部分，显式记录于此。

**验证证据（实际运行输出）**：`go build ./...` 成功；`go vet ./...` 无告警；`go test ./engine/ -run TestJudgeDanger -v` → **32/32 子用例 PASS**；`go test ./...` → `exit=0`，15 包 ok、0 FAIL；`go test -race ./engine/...` → ok；改动文件 `gofmt -l` 干净。

**本次未修、如实记录的同类缺口**：① `env -S 'bash -c "…"'`（`-S` 的值本身是一条命令串）不可判；② `bash -c "$CMD"` 等非字面量 payload；③ `bash script.sh`（脚本内容不可见）；④ 嵌套超过 `maxNestedShellDepth=1`；⑤ `xargs rm -rf` / `find -exec rm`；⑥ Windows（cmd/PowerShell）不在 POSIX 解析范围内。

---

## 7. 复核意见（2026-09-27，独立复核）

> 复核范围：§6 全部已实施改动 + §1/§2 编号条目的落地情况。
> 方法：通读全部 diff、跑 build/vet/test/race、用 `git show HEAD` 比对 gofmt 基线、写临时探针实测真实块形态（探针文件已删除）。
> **总结论：代码修复本身正确、可编译、测试全绿；但文档有 3 处事实错误、1 处遗漏、1 处未声明风险。其中 §6.3 声称已修复的"漏剥离"经实测不成立（见 7.2），必须更正——否则后续 agent 会误以为目录树/技能列表的回显问题已解决。**

### 7.1 复核通过（未发现问题）

| 项 | 结论 | 复核依据 |
|---|---|---|
| B1 门槛修复 | ✅ 正确 | `context/builder.go:96` 已为 `if a.userLangSet && a.systemPromptWithLang == ""`；`context/langdetect.go:114,154` 返回类型化值，Latin → `engine.LangEnglish`，故 `userLang != ""` 是冗余条件。英文会话拿到真实 system prompt（`TestBuild_EnglishSessionGetsSystemPrompt` PASS）。"附带发现"成立：生产路径下 `engine/turn.go:60` 的预热 Build 先于 `:72` 的实际 Build，且 `cmd/run.go:253` 的 compressor 恒非 nil，占位符不会外发。 |
| T1 类型化 | ✅ 彻底 | 全仓非测试代码已无 `"中文"` / `"日本語"` / `"English"` 字面比较，只剩 `engine/prompts_i18n.go:27-29` 的常量定义；`IsChinese()` 逐点替换，中文/日文路径行为等价。 |
| T2 最小修复 | ✅ 正确 | `engine/key.go:200-215` `infoQueryKey`；`engine/turn.go:762-770` 与 grep/glob/lsp 完全同机制；参数键名核对无误（`tools/builtin/search.go:68` = `query`，`tools/builtin/fetch.go:39` = `url`）。新增 2 用例 PASS。 |
| T5 最小修复 | ✅ 正确 | `engine/danger.go:132-146` 递归判定 + `:174-208` payload 提取；`judgeCall` 唯一调用点已带 depth（`:82`）。守卫映射核对：`dangerSystem → GuardBlock`、`dangerProject → GuardAskUser`（`engine/guards.go:155-171`），"效果链"成立。 |
| §6.2 工作区无关改动 | ✅ 属实 | `git diff go.mod` 与描述一致（删除 bubbletea replace，third_party 已暂存删除）。 |
| §6.1 gofmt 声明 | ✅ 属实 | `engine/agent.go`、`engine/compressor.go`、`engine/types.go` 在 HEAD（`git show HEAD:<file> \| gofmt -l`）即已被列出，非本次引入。 |

### 7.2 必须更正（P1）：§6.3 的"漏剥离已修复"与实测不符

§6.3「判定的 bug 1」把 `## 代码库结构` / `## Codebase`、`## 可用的 Skills` 等列为"手写清单漏掉、模型整块回显时不清除"的真实注入块，并称本次已修复。**实测不成立。**

原因：剥离语义是"命中标题行后删到下一个空行"（`engine/dsml.go:148-160`），而这两类块的**真实**形态恰好是"标题 → 空行 → 正文"：

- Block S 含代码围栏与目录树（`context/prompt.go:45-57`）：`## 代码库结构` 之后先是 1 行描述，再一个空行，才是围栏 + 目录树；剥离在空行处即停止。
- Skills 块的生产者是 `"## Available Skills\n"` + 介绍段 + 空行 + 列表（`cmd/run.go:149-150`）：标题后紧跟非空介绍行、再空行，剥离吃掉标题与介绍行，列表明细仍漏出。（原文此处记为 `engine/loop.go:404-409`，那是 `/skills` 命令输出、不进模型输入 —— 2026-09-27 引用勘误。）

临时探针实测输出（喂入真实块形态，探针文件已删除）：

```
Block S 回显 -> "```\ncmd/\n  run.go\n```\n\n真实结论。"
Skills 回显 -> "- **collab**: 并行研究\n- **ratd**: 红队测试\n\n使用 `/<名称>` 加载指定技能。\n真实结论。"
```

即：现在只认得出**标题**，注入内容的**主体**（目录树、技能列表、系统提示词正文）仍会漏给用户。`engine/dsml_internal_echo_test.go:9` 的 fixture 取了简化形态（省略围栏与目录树，且尾部无空行），因此测试 PASS 并未覆盖真实形态——**测试给了假阳性**。

§6.3 末尾的"已知缺口"其实已提到这一类（"标题后紧跟空行的块…只清掉标题，正文保留"），但与同节 bug#1 的"已修复"表述直接冲突，需二选一改正。

**修复方向（供后续决策，非本次改动）**：让剥离按**结构边界**而非文本标题——由生产者在块首/块尾写入不可见哨兵或显式 start/end 标记，剥离器按标记整段切除；或最低限度让 fixture 镜像真实生产者（含围栏与目录树），使该缺口在测试中可见。

### 7.3 文档硬错误（需更正数字 / 事实）

**P2 — §6.5 用例数错误。** 文中称"19 个表驱动用例"、"`19/19 PASS`"。实际 `engine/danger_test.go:21` 有 **32 个** case（`go test ./engine/ -run TestJudgeDanger -v` 展开 32 个子用例，全部 PASS）。

**P3 — §2-T3 行 120 的"漏条目"依据错误。** 文中把 `# Memory`、`## 关键发现`、`## 决策记录`、`## 待解决问题`、`## 假设` 列为"真实注入但不在清单"的漏剥离项。（2026-09-27 已更正 §2 该条并标注误报；同类引用错误还有一处：`## 可用的 Skills` / `## Available Skills` 的生产者应为 `cmd/run.go:149`。）实际 `memory.RenderMarkdown`（`memory/render.go:13`）全仓只被 `memory/store.go:101` 调用一次，用途是**把 `memory.md` 写到磁盘**，从不进入模型输入（内存内容以 TaskState 字段合并进引擎状态，见 `engine/loop.go:1068-1103`）。因此该类"漏剥离"不存在。修复本身按"派生自真实注入块"实现，结果无害，但该条依据应删除或标注为误报。

### 7.4 需补记（实现越界 / 未声明风险）

**P4 — §6.5 变动表遗漏了 `unwrap` 重写。** 表中只列 4 项，实际还重写了 `unwrap`（`engine/danger.go:436`）并新增 `wrapperFlagTakesValue`（`:482`）。（2026-09-27 行号勘误；该补充修复已作为 §6.6 记入本文件。）这不止影响 shell payload，而是改变了 `env` / `nohup` / `command` / `builtin` / `time` **所有**包装命令的判定口径（方向更严；`env -i` / `env -u` / `command -p` 已被测试覆盖，`command -v` 无误报）。属超出"最小修复"叙述的范围，应显式记录。

**P5 — T3 新增的过删面未声明。** 派生集合现在包含系统提示词、语言包、AGENTS.md 的**全部标题**，于是 `## Guidance`、`## Go Rules`、`# 边界`、`# 身份`、`## 环境` 这类通用标题也成为删除触发词。整行相等只是缓解而非消除。文档只论证了"删掉死条目 ⇒ 减少误删"，未提新增的这一面。

**P6 — 架构观察（非阻塞）。** `tools/registry.go:10` 本次**新增** `import engine`（为使用 `engine.UserLanguage`）。check-archs 的分层规则禁止 tools 核心文件 import engine，但本仓 `docs/superpowers/specs/2026-09-18-pipeline-skills-and-docs-sync-design.md:33` 已明确该规则与真实依赖图不符（engine 是类型中枢），且 `tools/subagent.go` 早已如此；无循环依赖（engine 不 import tools/context）。记录备查，不要求改动。

### 7.5 复核使用的实际命令与输出

```
go build ./...                                            -> exit 0，无输出
go vet ./...                                              -> 无告警
go test ./...                                             -> exit 0，15 包 ok、0 FAIL
go test -race ./engine/... ./context/... ./tools/...       -> exit 0
go test ./engine/ -run TestJudgeDanger -v                  -> 32/32 子用例 PASS
go test ./engine/ -run 'TestExecuteTurn_MadeProgress_NovelWebSearch|TestExecuteTurn_MadeProgress_RepeatedFetch|TestStripInternalPromptEcho|TestInternalEchoHeaders_LineQualification'
                                                          -> 4 个用例全 PASS
gofmt -l engine/agent.go engine/compressor.go engine/types.go        -> 3 个文件被列出
git show HEAD:engine/agent.go | gofmt -l                             -> 同样被列出（非本次引入）
临时探针 engine/zz_probe_echo_test.go（已删除）                       -> 输出见 7.2
```

**行号说明**：本节行号基于 2026-09-27 的工作区快照（含 §6 全部改动）。§1/§2 中的行号仍是改动前的旧位置。
