# DeepAct — Implementation TODO

> 基于 `DESIGN.md` 与代码实际实现的全量差距分析。
> 已验证代码文件 38 个，覆盖 `engine/` `policy/` `context/` `router/` `session/` `cmd/` 等核心包。

---

## ✅ Done (已实现，代码已读验证)

### Phase 1 MVP — 全部完成
| 功能 | 代码位置 |
|---|---|
| CLI 入口 (Cobra) | `cmd/root.go`, `cmd/run.go`, `cmd/exec.go` |
| 交互式 TUI (Bubble Tea) | `ui/model.go`, `ui/runner.go` |
| Headless/CI 模式 | `cmd/exec.go` |
| API Key 管理 | `cmd/apikey.go`, `cmd/set.go` |
| 5-stage 引擎循环 | `engine/loop.go:68-203` |
| Ambiguity Gate | `policy/ambiguity.go:22-52` |
| Design Guard (LLM 单次审查) | `policy/checker.go:33-61` |
| Loop Guard + Tool Dedupe | `engine/guards.go:26-63` |
| Scope Guard | `engine/guards.go:75-95` |
| 双模型路由 (简化 RiskScore) | `router/selector.go:18-51` |
| DeepSeek API (streaming/retry/limiter) | `llm/deepseek.go`, `llm/retry.go`, `llm/limiter.go` |
| Token 估算 | `llm/token.go` |
| 4 层压缩 (Layer 3 简化版) | `engine/compressor.go:24-134` |
| 书签式 Prompt 布局 | `context/builder.go`, `context/prompt.go` |
| 语言包 (Go/TS/Python/Rust/Java/Generic) | `context/langpacks.go` |
| 6 个核心工具 | `tools/builtin/{read,write,edit,grep,glob,bash}.go` |
| Session 持久化 (JSONL) + Fork/Rewind | `session/store.go`, `session/rewind.go` |
| Artifact CAS (SHA256) | `artifact/store.go` |
| AGENTS.md 级联加载 | `context/builder.go:87-118` |
| 多 Agent 编排 (关键词分发) | `engine/orchestrator.go:57-112` |
| 4 个内置子 Agent | `engine/default_agents.go:6-37` |
| Self-challenge | `engine/challenge.go` |
| DSML fallback 解析 | `engine/dsml.go:19-68` |

---

## 🔴 High Priority (Phase 2 核心缺失)

| # | 功能 | 设计文档依据 | 代码证据 |
|---|------|------------|---------|
| 1 | **LSP 集成** — go-to-def, references, symbols | DESIGN.md §8.3, §9.5 | 无 `retrieval/` 目录，go.mod 无 LSP 依赖 |
| 2 | **AST-aware 代码折叠 (Layer 3)** — tree-sitter 签名提取 | DESIGN.md §8.4 Layer 3 | `compressor.go:104` 用 `strings.Contains("file:")`，go.mod 无 tree-sitter |
| 3 | **完整 RiskScore 公式** — 缺 ambiguity/security/grounding 因子 | DESIGN.md §3.4 | `router/selector.go:30-51` 仅 3 个因子，设计要求 5 个 |
| 4 | **Decision Agent + 确认协议** — L0-L3 自主权分级 | DESIGN.md §13.1-13.3 | 无 `decision_agent.go`，无 `HandoffResult.Confidence` |
| 5 | **Progressive Trust** — 偏好自动学习 | DESIGN.md §13.4 | 无 `preferences.toml`，无信任计数器 |
| 6 | **TOML 配置加载** — 级联 config 系统 | DESIGN.md §11.2 | go.mod 无 Viper，`cmd/run.go:74` 全部硬编码 |

---

## 🟡 Medium Priority (Phase 2 完善项)

| # | 功能 | 设计文档依据 | 当前状态 |
|---|------|------------|---------|
| 7 | **Findings Board** — Agent 间共享检查点 | DESIGN.md §12.3 | 无 `FindingsBoard` 结构体，无 pub/sub |
| 8 | **Call Chain Discovery** — LSP 递归调用链追踪 | DESIGN.md §8.3 | 依赖 LSP，当前无实现 |
| 9 | **动态任务分解 (LLM-based)** — Pro 模型分解查询 | DESIGN.md §12.2 | `orchestrator.go:63-78` 用 `strings.Contains` 关键词匹配 |
| 10 | **Permissions 系统** — auto/ask/deny 三级模式 | DESIGN.md §11.2 | `policy/permissions.go` 未创建 |
| 11 | **Security 路由信号** — 安全关键词→Pro 升级 | DESIGN.md §3.2 | `policy/security.go` 未创建 |
| 12 | **本地文档 Grounding Level 2-3** | DESIGN.md §9.5 | 无实现，依赖 LSP |
| 13 | **Design Guard 双通道 + 静态分析 backstop** | DESIGN.md §9.2 | 单次 LLM 审查，无静态分析 |
| 14 | **Output Schema 强制** — `{summary,changes,next_step,questions}` 长度上限 | DESIGN.md §9.3 | `EngineResponse` 有结构但无校验 |
| 15 | **Forced Rebase 每 5 轮** | DESIGN.md §9.3 | `compressor.go:24-39` 按 token 比例触发 |

---

## 🟢 Low Priority (Phase 3 / 工具链)

| # | 功能 | 说明 |
|---|------|------|
| 16 | **MCP Tool 支持** | `tools/mcp/` 目录未创建 |
| 17 | **SQLite 多 Agent 协调** | 无 `memory/` 目录，无 goals/tasks/artifacts 表 |
| 18 | **JSON-RPC Server 模式 (IDE)** | `app/modes/server.go` 未创建 |
| 19 | **Platform Sandbox** | 无 Seatbelt/seccomp/AppContainer |
| 20 | **OpenTelemetry 审计日志** | 无 `otel` 依赖 |
| 21 | **Web Search MCP** | 设计明确推迟到 Phase 3 |
| 22 | **`cmd/plan.go`** — Plan-only 模式 | 未创建 |
| 23 | **`cmd/session.go`** — Session 管理 CLI | 未创建 |
| 24 | **`cmd/config.go`** — 配置管理 CLI | 未创建 |
| 25 | **GoReleaser 分发** | 无 `.goreleaser.yaml` |

---

## 📋 推荐执行顺序

```
第 1 步: 修复 AutoConfirmScope: true → false (cmd/run.go:138)
第 2 步: 补充核心测试 (engine loop, policy, router)
第 3 步: TOML 配置加载器 (config/loader.go)
第 4 步: LSP 集成 (retrieval/lsp.go — 最小可用: gopls)
第 5 步: AST-aware 代码折叠 (tree-sitter)
第 6 步: 完整 RiskScore 公式
第 7 步: Decision Agent + 确认协议
第 8 步: Progressive Trust
第 9 步: Findings Board
第 10 步: GoReleaser + CI
```

---

## 🐛 已知缺陷

| # | 问题 | 位置 | 严重度 |
|---|------|------|--------|
| 1 | `AutoConfirmScope: true` — Scope Guard 实际上不工作 | `cmd/run.go:138` | **高** |
| 2 | `reasoning_content` 未验证就发送 — 可能 400 错误 | `llm/thinking.go` | **高** |
| 3 | Token 估算用 `len/3` — 无 CJK 校准 | `context/builder.go:70` | 中 |
| 4 | Full compact 未显式指定 non-thinking | `engine/compressor.go:116-134` | 中 |
| 5 | Sub-agent 结果以 user message 注入 — 可能混淆角色 | `engine/loop.go:140-145` | 低 |
