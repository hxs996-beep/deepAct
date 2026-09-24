# 子代理只读宇宙设计（Sub-agent Read-only Universe）

日期：2026-09-24
状态：已实施（单 PR；含两项破坏性变更，见末节）

## 问题

委派链上不存在任何权限不变量：

1. **危险命令守卫绕过**：`ScopeGuard.CheckTool`（guards.go）只有主循环 `turn.go` 一个调用点；子代理 `runLoop` 直接 `r.tools.Execute`。主循环需要人审的命令（`git push --force`、`rm -rf`…）可以经 `handoff_to_agent` 无审查执行。更根本的：`tools/registry.go` 的 `Execute` 按名直查全量注册表，不校验本次会话是否提供过该工具——注入/幻觉诱发的 `bash` call 今天就会执行。
2. **白名单只是默认值**：`specSubAgent.Run` 只在委派方未传 `tools` 时才用 `spec.ToolNames`；`HandoffToAgentParams.Tools` 允许 per-call 覆盖，researcher 的只读约束可被一条参数解除。

## 决策

**子代理全局只读（能力缺席），而不是把危险判定复制到子代理路径（场景枚举）。** 子代理 = 探索/分析/产出结论；修改属于主代理（其 bash 仍过 danger guard）。这是 AGENTS.md 第一性原理的正解：消除一整类能力（子代理的修改权）收敛问题，而不是拦截一张命令清单。ratd 管道（proposer/redteam 只读、编排者落盘）是既有先例。

曾评估并否决的第三方案：复用 `ScopeGuard` + `ask_user` 气泡做"危险命令任意深度人审"。否决理由：只护 bash 不护 write/edit；气泡使子代理以 `awaiting_user` 终止、history 焚毁，按命令粒度 ping-pong；`pendingAskUser` 单槽在并行下互相覆盖；工程量逼近 dsh 的四层守卫机器而收益只是"与主循环同水位"。

与 c52396b（删除引擎侧写死判定）的方向张力，适用边界论证：被删的是行为预测（归模型），宇宙是委托执行的信任边界（AGENTS.md 为"安全/数据破坏/不可逆"保留的固定模式例外）。判定标准：删掉约束后最坏情形是"行为不理想"→ 交模型；是"不可逆数据破坏"→ 确定性保护。该判据仅适用于安全类约束，不泛化为通则。

## 宇宙与公式

```
effective(child) = 只读宇宙 ∩ 注册表 ∩ (role.ToolNames 非空时) ∩ (params.tools 传入时)
通道（handoff_to_agent / ask_user / submit_result）恒在 effective 集内
```

- 只读宇宙（闭集）：`read, read_multi, grep, glob, lsp, web_search, fetch`
- 排除（注册表成员）：`bash/write/edit/revert/artifact/skill_install` + 全部 MCP（`<server>_<tool>` 命名，不在枚举内即被过滤，无需识别来源）
- 排除（spec-only，本就不在注册表）：`plan_task/task_complete/agent_poll/todo_write`
- 已知缺口：`load_skill` 不在注册表（主循环以 spec 追加 + 按名拦截），子代理今天无法加载技能——跟进项：在 runLoop 仿 ask_user 拦截。
- 闭集对未知默认安全：新内置工具/新 MCP server 自动排除。
- 剩余风险：名字基白名单不区分来源。MCP server `web` + tool `search` → 注册名 `web_search` 穿透。撞名要求用户把 MCP 命名成与内置工具相同，属自伤配置而非注入面；接受。跟进硬化项（可选）：`ToolSpec` 加来源标记，宇宙校验 `source==builtin`。

## 强制点（双闸）

1. **可见性闸**：`filterTools` 对非通道 spec 施加宇宙成员检查——越权工具从子代理提示中消失。委派方请求宇宙外工具/空交集时 `specSubAgent.Run` fail-loud，错误文案承担模型教育（双语，指明可用工具与修改归属）。
2. **执行闸（安全主张的承载点）**：`runLoop` 在 `Execute` 前校验 `call.Name ∈ effectiveSet`。可见性不是安全边界——注册表按名解析，幻觉/注入的越权 call 必须在此被确定性拒绝。被拦调用写入 `Blocked:` tool 消息（教育性：列出本 run 可用工具，防模型试探烧轮次）。

**被拦连击终止**：`consecutiveBlocked` 只计连续被拦，任何实际派发重置；≥3 → `FinishReason=loop_detected`（复用既有常量，digest heading 已处理）。被拦轮计入 `MaxIterations`（iter++ 天然覆盖）。`:482` 的叙述计数清零改为条件清零（被拦调用 = 无进展）。

**无外层兜底**：`turn.go` 对任意 handoff 无条件置 `MadeProgress=true`，主循环 breaker 不会因重复委派触发——执行闸自身是唯一终止点，这是闸必须自带终止判定的直接理由。

## 配置

`[agents.<role>].tools` 在 `*` 剥离后经 `engine.ValidateSubAgentTools` 校验，宇宙外工具启动即报错（文案带迁移指引：删除写类工具，修改由主代理执行）。`["*"]` → 空 ToolNames = 全宇宙（语义从"全部工具"收窄为"全部只读工具"）。

## 破坏性变更（release notes 各一条）

1. `[agents]` 角色的 `tools` 含 `bash/write/edit/revert/skill_install/MCP` → 启动失败（含 `*` 语义收窄）。错误文案与 README `[agents]` 段同步迁移指引。
2. `sub` 角色语义变化：从"全能执行者"改为"通用只读分析员"（保留 `StructuredResult: true`；与 researcher 证据导向调查、critic 对抗审查三分差异化）。依赖旧行为的会话需把修改工作收回主代理。

模型可见面同步四处：动态 schema `tools` 参数与 `async` 描述（删除 "builds, tests, batch scripts"——子代理已不能跑构建/测试）、`tools/subagent.go` 硬编码 sub 兜底描述、静态回退 `handoffToolSpec`（双语 + enum 补 ratd 角色）。工具描述进 stable 前缀，一次性缓存失效，可接受。

## 与 deepseek-harness 的对照

dsh 用四层运行时守卫链（capability 断言 + 工具过滤双闸 + 审批钉死 'never' + sandbox/auto-review）买"子代理可写"的灵活性；本方案用能力缺席买简单性——在没有那四层基础设施的单用户终端工具里，缺席是唯一站得住的选择，且比 dsh 多一个性质：闭集对未知默认安全。殊途同归的部分：fail-loud 三连、模型可见边界文案、安全属性不由单次委派决定。将来若引入外部子代理后端，按 dsh 模式 fail-loud 拒绝（`NO_START_CAPABILITIES` 式），不承诺对端过滤。

## 测试

`engine/sub_agent_universe_test.go`（9 用例：默认 offered 集、越权 fail-loud、收窄、researcher 回归、执行闸拒发+存活、3-strike/重置、嵌套同闸、角色交集、MCP 形排除 + 撞名残留、并发 `-race`）+ `config_test.go` TOML 校验三例。既有 bash-stub 机制测试适配为 read（机制不变，工具名任意）。
