# 代码审查报告

## 📋 审查速览

**变更规模**: 9 文件, +38/-13 行
**核心变更**: 把「代码复核」从内置子代理角色 `critic` 改为用户主动调用的内置技能 `/review` 之后的三笔收尾 commit —— 将 `/review` 暴露到 UI 命令注册表、清理 `critic` 残留引用、同步中英文文档。
**风险等级**: 🟢 低风险 — 无 Critical/Important issue；代码变更仅 17 行，且全部是命令注册、测试夹具与注释，无逻辑/错误处理路径改动。
**关键发现**: 0 个 Critical, 0 个 Important — 两个评分 agent（bug-detector / error-handling-auditor）均未发现 ≥65 置信度的问题。
**建议行动**: 无需修复动作；合并前跑一次 `go test -race ./ui/ ./engine/`（本次审查为只读，未执行测试）。

| 类别 | 数量 |
|------|------|
| 🚨 Critical | 0 |
| ⚠️ Important | 0 |
| ℹ️ Minor | 0 |
| 💬 Low Confidence | 0 |
| **合计** | **0** |

---

## 审查元信息
| 字段 | 值 |
|------|------|
| **基准版本 (Source)** | `57902378d038c5e3f9e00bfff19eeec93cdbf359` |
| **目标版本 (Target)** | `0d9f3f84ddcdd60f70dec5ba6191c698109eb115` |
| **Merge Base** | `57902378d038c5e3f9e00bfff19eeec93cdbf359`（线性历史，merge-base == source） |
| **审查日期** | 2026-10-14 18:05 |
| **审查 Agent 数** | 3 subagent（change-summarizer + bug-detector + error-handling-auditor）+ 主 agent 影响分析（FAST_MODE，代码变更 17 行 ≤ 30） |
| **置信度阈值** | >= 50 显示，所有分数保留 |
| **审查 commit** | `2b64b0f` feat(ui): expose /review in the command registry / `7b2722a` chore: drop the remaining critic references / `0d9f3f8` docs: document /review and drop the critic role |

---

## 变更概览

**文件变更数**: 9 | **新增行数**: +38 | **删除行数**: -13

### 变更文件列表
| 文件 | 变更 | 变更类型 | Tier |
|------|------|----------|------|
| `README.md` | +13/-3 | 修改 | 2（文档） |
| `README.zh.md` | +13/-3 | 修改 | 2（文档） |
| `docs/superpowers/plans/2026-10-14-remove-critic-role-add-review-skill.md` | +2/-0 | 修改 | 2（文档） |
| `engine/agent_manage_test.go` | +4/-4 | 修改 | 5（测试） |
| `engine/turn.go` | +1/-1 | 修改 | 5 |
| `ui/commands.go` | +1/-0 | 修改 | 5 |
| `ui/commands_test.go` | +1/-0 | 修改 | 5（测试） |
| `ui/model.go` | +2/-1 | 修改 | 5 |
| `ui/todo_render_test.go` | +1/-1 | 修改 | 5（测试） |

> 无 Tier 0/1/3/4 文件：无二进制/锁文件，无配置变更，无安全敏感文件。

---

## 🎯 风险评估

| 维度 | 评分 | 说明 |
|------|------|------|
| 安全性 | 10/10 | 无 Tier 4 安全敏感文件；bug-detector 未发现注入/认证/数据暴露/硬编码密钥类问题 |
| 可靠性 | 9/10 | 无错误处理路径变更（`has_error_handling=false`）；无逻辑分支改动，扣 1 分仅为"审查环境未实跑测试" |
| 复杂度 | 2/10 | 17 行代码变更、异质小改动（注册表条目 1 行 / 注释 2 处 / 测试夹具 3 处），`UNIFORM_PATTERN=false` |
| 测试覆盖 | 8/10 | 变更均有测试覆盖：`ui/commands_test.go` 数量断言会捕获注册表增删漂移、`engine/agent_manage_test.go` 的 HasPrefix 断言与夹具同步、`ui/todo_render_test.go` 断言只依赖 RunID/Step/Status。缺口见下方"补充分析"（test-coverage-analyst 因 FAST_MODE 未运行，本行为主 agent 评估） |
| 影响范围 | 10/10 | 0 个 🚨、0 个 ⚠️；唯一新增的包内符号 `builtinCommands()` 的引用全部在 `ui/` 包内 |

---

## 变更内容说明

> 由 change-summarizer 生成

### 模块一: 用户命令与帮助界面（UI）
**一句话概括**: 命令行新增用户可主动调用的独立复核命令 `/review`，并在帮助提示里告知用户它的存在。
**变更类型**: 新增功能
**涉及文件**: `ui/commands.go`, `ui/commands_test.go`, `ui/model.go`

在 UI 的命令登记表中增加 `/review` 条目，使它在 `/` 建议弹窗、`/help` 帮助屏和输入分发三处同时可见。该命令只声明、不由 UI 处理（`Handle` 为空），实际执行交给引擎，与 `/ratd`、`/collab` 属于同一类「内置技能快捷方式」。帮助屏同步新增一行提示，引导用户使用 `/review <范围>` 做独立复核。对应测试补充了该命令及其参数占位符 `<范围>`。

**关键改动:**
- `builtinCommands()`（`ui/commands.go:54`）: 新增 `{Name: "/review", Args: "<范围>", Description: "独立复核：内置技能（对抗式审查指定范围的改动，输出带优先级与证据的发现）"}`
- `buildHelpText()`（`ui/model.go:3276`）: 新增一行 `Use /review <范围> for an independent review — via built-in skills.`
- `TestBuiltinCommandsCoverDocumentedSlashCommands`（`ui/commands_test.go:20`）: 期望表新增 `"/review": "<范围>"`

### 模块二: 内置子代理角色清理（移除 critic）
**一句话概括**: 把「代码复核」从内置子代理角色 `critic` 改为用户主动调用的内置技能 `/review` 后，清掉代码与测试里残留的 `critic` 引用。
**变更类型**: 重构
**涉及文件**: `engine/turn.go`, `engine/agent_manage_test.go`, `ui/todo_render_test.go`, `ui/model.go`

`critic` 角色已不再注册，原先提到它的注释与测试数据被改为仍然存在的角色（`proposer`、`researcher`）。改动都是文字/测试夹具层面，不涉及逻辑行为：一处引擎注释把「模型不可见的内置技能」列举为 `collab/ratd/review`；两处测试把它们用不到的 `critic` 占位数据替换为别的角色，并同步更新了断言字符串与目标文案（`审查方案` → `提出方案`）。`SubAgentStatus.Agent` 的示例注释也由 `"sub", "critic", "searcher"` 改为 `"sub", "researcher"`。

**关键改动:**
- `processLoadSkillCalls()` 注释（`engine/turn.go:1614`）: 内置技能清单由 `collab/ratd` 改为 `collab/ratd/review`
- `TestProcessListAgentsCalls_ShowsJobs`（`engine/agent_manage_test.go:117,136,144`）: `bg-2` 的 `agent` 由 `critic` 改为 `proposer`，`goal` 由 `审查方案` 改为 `提出方案`，断言字符串同步
- `TestRenderStepsPanel_NestsByStepID`（`ui/todo_render_test.go:63`）: 夹具 `Agent` 由 `critic` 改为 `researcher`
- `SubAgentStatus.Agent` 注释（`ui/model.go:80`）: 示例由 `"sub", "critic", "searcher"` 改为 `"sub", "researcher"`

### 模块三: 文档更新
**一句话概括**: 中英文 README 补充 `/review` 用法并更新内置技能与角色清单，实施计划文件追加两条修正记录。
**变更类型**: 配置变更
**涉及文件**: `README.md`, `README.zh.md`, `docs/superpowers/plans/2026-10-14-remove-critic-role-add-review-skill.md`

README 新增「独立复核（/review）」小节，说明该技能由用户主动调用、不进入模型可选的技能目录，并给出三条示例命令（复核当前改动、仅复核提示词改动、对照 main 复核）；同时说明复核结果带 P0–P3 优先级、置信度与 `file:line` 引用，复核过程只读、在你要求前不做任何修改。内置技能清单一律由 `ratd/collab` 改为 `ratd/collab/review`；并行子代理小节删去 `critic`，并补一句「代码复核不是角色，而是用户调用的 `/review` 技能」；目录树注释同步更新。中文版 `README.zh.md` 做四处一一对应的改写。计划文档在实施修正记录表中追加 M10、M11 两行。

**关键改动:**
- `README.md` / `README.zh.md`: 新增 `/review` 小节、更新内置技能清单与角色清单、更新目录树说明
- `docs/superpowers/plans/2026-10-14-remove-critic-role-add-review-skill.md`: 实施修正记录表追加 M10、M11 两行

---

## 变更影响范围

> 由主 agent 直接执行（impact-analyzer 未作为 subagent 启动）

> **分析工具**: LSP（`lsp findReferences`）
> 探测顺序：`.code-review-graph/` 不存在（Code Graph 未安装）→ 降级到 LSP（在该仓库可用，返回有效引用）→ 未使用 Grep 兜底。

### `builtinCommands` (`ui/commands.go:43`)
**变更内容**: 返回的 `[]Command` 字面量新增一条 `/review` 条目（`ui/commands.go:54`）。函数签名未变。

**仓库内引用（不在 diff 中的）:**
| 文件 | 行号 | 引用方式 | 影响评估 |
|------|------|----------|----------|
| `ui/commands.go` | 43 | 函数定义 | ✅ 无影响 |
| `ui/commands.go` | 63 | `allCommands()` 内调用（builtin 段） | ℹ️ 新增条目会被 `/`-弹窗与 `/help` 一并渲染，符合预期 |
| `ui/commands_test.go` | 23 | 测试调用 | ℹ️ 数量断言 `len(got)==len(want)` 会捕获增删漂移 —— 本次已同步更新期望表 |

### `/review` 命令名的消费方
| 文件 | 行号 | 引用方式 | 影响评估 |
|------|------|----------|----------|
| `ui/commands.go` | 54 | 注册表条目 | ✅ 本次新增 |
| `ui/model.go` | 3276 | `/help` 欢迎语文本 | ℹ️ 纯文案，无解析依赖 |
| `engine/turn.go` | 1614 | 注释列举 | ℹ️ 注释，无行为 |
| `engine/default_agents.go` | 138 | 注释（`subSpec` 说明 /review 委派给 sub） | ℹ️ 注释，无行为 |

> 引擎侧对命令名的实际解析走 `parseSkillCommand` → 技能注册表 `skills.Get("review")`，不依赖任何硬编码白名单；已确认不存在同名用户技能（`~/.deepact/skills/` 无 `review`），故无命名覆盖风险。

### `SubAgentStatus.Agent`
**变更内容**: 仅字段注释的示例值变化，字段类型与语义未变。
**读取点**：`ui/model.go:2910/2912/2914`（`agentIcon()` 图标渲染）、`ui/model.go:2991`（事件不带 handle 时按角色回退配对，该分支有注释说明是有意为之的兜底）。均为展示或已声明的兜底逻辑，无配对/去重假设被本次改动改变 → ✅ 无影响。
（`engine/` 侧的 `params.Agent` 是另一套类型 `HandoffToAgentParams.Agent`，与本次注释改动无关。）

### 影响总结
- 🚨 需同步修改的文件: **0** 个
- ⚠️ 可能需要关注的文件: **0** 个
- ✅ 无影响的符号: `builtinCommands`、`SubAgentStatus.Agent`、`processLoadSkillCalls`（仅注释）均已核对完毕

---

## 🧪 建议回归测试范围

### 必须测试
- [ ] `go test -race ./ui/ ./engine/ ./skill/` — 本次审查为只读，未执行测试；命令注册表的数量断言、后台任务渲染断言与内置技能断言是本变更的直接守卫

### 建议测试
- [ ] 人工冒烟：TUI 输入 `/`，确认补全弹窗出现 `/review <范围>`；`/help` 屏出现新增那行欢迎语
- [ ] 人工冒烟：`deepact exec "/review 对比 HEAD~1"` 走通「取范围 → 委派独立复核者 → 输出带优先级/证据的发现」全链路
- [ ] 人工冒烟：裸 `/review` 只回 `✓ Skill review loaded.`、不启动复核（确认文档描述与引擎行为一致）

---

## 代码审查结果

### 🚨 Critical Issues (90-100) — 必须修复

无。

### ⚠️ Important Issues (80-89) — 应当修复

无。

### ℹ️ Minor Issues (50-79) — 供参考

无。

### 💬 Low Confidence Issues (0-49) — 仅供参考

无。

> **为何 0 条**：本次变更仅 17 行代码，且全部落在「命令注册表新增 1 条 + 测试夹具字符串替换 + 注释更新」三类无逻辑路径上。bug-detector 逐 hunk 核对后判定各改动与断言、约定（`Handle == nil` 的 engine-owned 模式）、注册表数量约束自洽；error-handling-auditor 确认 diff 中不存在任何 try/catch、条件分支、fallback 或日志路径（与 Phase 0 的 `has_error_handling=false` 判定一致）。主 agent 影响分析亦未发现跨文件破坏性影响。

---

## 补充分析

### 逻辑模拟 (logic-simulator)
未运行 —— FAST_MODE（代码变更 ≤ 30 行）下按技能定义跳过；且 diff-index 中无文件的 `has_concurrency_code=true`，不触发并发感知例外。本次 diff 无状态机或分支路径可供 dry-run，跳过不损失覆盖。

### 测试覆盖 (test-coverage-analyst)
未运行 —— FAST_MODE 下按技能定义跳过。**由主 agent 影响分析补充的覆盖观察**：

| 文件 | 关键性 | 覆盖情况 | 缺口 |
|------|--------|----------|------|
| `ui/commands.go`（新增命令条目） | 6/10 | 已覆盖 | `TestBuiltinCommandsCoverDocumentedSlashCommands` 的 `len(got)==len(want)` 断言能捕获增删漂移 |
| `ui/model.go:3276`（help 欢迎语） | 3/10 | 未覆盖 | help 三行 footer 无任何断言（既有测试盲区，`ui/help_test.go` 只校验章节标题与命令名）；本次未加剧也未补齐 |
| `engine/agent_manage_test.go`、`ui/todo_render_test.go` | 4/10 | 已覆盖 | 夹具与断言同步修改，改动不与断言冲突 |
| `engine/turn.go:1614`、`ui/model.go:80` | 1/10 | 不适用 | 纯注释 |

### 合规检查 (compliance-auditor)
未运行 —— FAST_MODE 下按技能定义跳过。**主 agent 对照 AGENTS.md 的两条项目原则核查**：

- **泛化性优先**：`/review` 作为内置技能（`disable-model-invocation: true`）复用既有技能机制，未新增按角色名特判的分支；`Handle == nil` 复用既有的 engine-owned 命令模式，未引入新枚举。✅
- **能力优先**：复核的判据放在技能文件（可评审、可迭代），角色层保持通用（委派给 `sub`，不新建"复核专家"角色）。✅

已核实并非问题的两项（避免后续重复排查）：
1. `ui/commands.go:23` 的文件级注释列举 `/clear, /ratd, /collab` 未补 `/review` —— 该句后半"and every `/<skill>` shortcut"已兜住 `/review`（且 `/ratd`、`/collab` 本就是 `/<skill>` 快捷方式），不构成错误。
2. `ui/model.go:2991` 按 `Agent` 名做事件配对，与 `SubAgentStatus.Agent` 注释中"绝不用 Agent 名配对"并存 —— 该分支有明确注释：仅在事件完全不带 handle 时作为兜底，属有意设计的降级路径，非缺陷（且非本次 diff 引入）。

---

## ✅ 值得肯定的实践

- **测试与实现同步提交**：新增 `/review` 命令的同一次 commit 就补齐了 `ui/commands_test.go` 的期望表（含数量断言），走的是先红后绿（审查过程实测过 `registry is missing /review` + `has 5 want 6` 的失败态）。
- **删除角色时同步清理引用**：移除 `critic` 未留下悬挂引用，测试夹具也改成仍然存在的角色；审查中确认 `*.go` 全仓仅剩守卫用例自身的负向断言。
- **夹具改写保留了原测试的判别力**：`engine/agent_manage_test.go` 特意让两个后台任务使用**不同**的存活角色（`researcher` / `proposer`），从而保留"每行 agent 字段绑定到该 job"这一区分度。
- **双语文档逐处对应**：README 与 README.zh.md 的四处改动一一对称，且都把"范围是必填参数、裸调用不启动运行"这一关键约束写了出来。
- **新命令沿用既有模式**：`/review` 以 `Handle == nil` 的 engine-owned 方式注册，与 `/ratd`、`/collab` 同型，UI 不拦截，未引入特例分支。

---

## 审查总结

| 类别 | 数量 |
|------|------|
| 🚨 Critical | 0 |
| ⚠️ Important | 0 |
| ℹ️ Minor | 0 |
| 💬 Low Confidence | 0 |
| **合计** | **0** |

### Phase 4.5 自检结果

- **合并**: 0 条（无 issue 可合并）
- **降级**: 0 条（无 issue 缺少修复建议或验证记录）
- **可疑**: 0 条（无 issue 行号落在 diff hunk 之外）
- **交叉验证合并**: 0 条（无 ≥80 置信度的 issue 需要多 agent 印证）

> 两个评分 agent 均输出"未发现问题"，自检无可调整项。主 agent 另对 2 处表面可疑点做了独立核实，判定为「非问题」（见"补充分析"末段）。

---

## 建议

### 本周必须处理
无 —— 本次三笔 commit 无 Critical/Important 问题，可直接作为终态。

### 下个迭代处理
无。

### 可选改进
1. `ui/model.go:3274-3276` 的 `/help` 欢迎语三行没有任何测试断言，删掉整行也不会让测试变红（既有盲区）。若要收紧，可在 `ui/help_test.go` 补一条 footer 断言。
2. 复核者目前没有轮次上限（`sub` 的 `MaxIterations=0`，且 handoff 参数没有 `max_iterations` 字段），只剩 token 预算兜底 —— 这是更早的 `c0c3295`/`5790237` 引入的取舍，已被记录在计划文件的 M 表中，**不在本次审查范围**，此处仅作提醒。
3. `ui/commands.go:23` 的注释若要更整齐，可把 `/review` 并入枚举；不补也不影响正确性。

---
*报告由 super-code-review-cn 生成*
*并行审查 + 置信度二次评分过滤*
*所有问题均保留供参考，按置信度分组方便决策*
