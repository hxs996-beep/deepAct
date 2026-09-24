# DeepAct — 轻量级终端 AI 编码代理 · 为 DeepSeek 而生

**简体中文** | [English](README.md)

<p align="center">
  <a href="https://goreportcard.com/report/github.com/deepact/deepact"><img src="https://img.shields.io/badge/go_report-A-brightgreen?style=flat-square" alt="Go Report"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=flat-square" alt="MIT"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go" alt="Go 1.24+"></a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey?style=flat-square" alt="Platforms">
</p>

<p align="center">
  <b>⚡ 单二进制 · 零运行时依赖 · DeepSeek 原生</b>
</p>

**DeepAct 是一个跑在终端里的 AI coding agent（AI 编码代理）**：用 Go 编写、静态编译、开源（MIT）、为 DeepSeek API 全链路调优。没有 Node、没有 Python、没有 Docker——一个二进制加一个 DeepSeek API Key 即可。

---

## 📖 目录

1. [快速开始](#快速开始)
2. [日常使用](#日常使用)
3. [对接 DeepSeek](#对接-deepseek)
4. [核心能力](#核心能力)
5. [CLI 命令一览](#cli-命令一览)
6. [架构](#架构)

---

## 🚀 快速开始

> [!NOTE]
> 需要一个 [DeepSeek API Key](https://platform.deepseek.com/)。首次启动时 TUI 会交互式提示输入，并自动持久化到 `~/.deepact/config.toml`——无需手动配置步骤。

### 安装

```bash
# macOS / Linux 一键安装
curl -sSfL https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.sh | sh

# 或 Go
go install github.com/deepact/deepact@latest
```

Windows 用户见 [Releases](https://github.com/hxs996-beep/deepAct/releases)（PowerShell 或手动下载）。

### 开始使用

```bash
deepact                      # 启动交互式 TUI（Windows / macOS / Linux 通用）
deepact exec "修复连接池竞态"  # 非交互 / CI 模式
```

环境变量 `DEEPSEEK_API_KEY` 优先级最高；项目级配置 `.deepact/config.toml` 覆盖全局配置。

## 🖥️ 日常使用

### TUI 快捷键

| 按键 | 作用 |
|------|------|
| `Ctrl+Q` | 退出 |
| `Esc` | 取消当前任务 / 清空输入 |
| `Enter` | 提交 |
| `Shift+Enter` | 换行 |
| `Tab` | 补全 |

### 一句话开工（命令行直出）

```bash
deepact exec "给 LoginHandler 加超时和熔断"
deepact exec "把 user 表迁移到 Postgres 并修好所有编译错误"
deepact exec "review 最近 5 个 commit 的潜在 bug"
```

### 并行研究（/collab）

```bash
deepact exec "/collab 加一个缓存层"
```

`/collab` 是内置协作技能，由 `handoff_to_agent` 编排：主 agent 自己把目标拆成 2~6 个研究方向，然后**并行**把每个方向委派给 `researcher` 子代理（只读工具），再把各方向发现合并成一份结构化报告——需要快速摸清广度时，比串行调研更快。

### 项目规范与技能（Skills）

项目规范、工作流、领域知识通过**技能**注入系统提示：技能列表渲染进稳定区，Agent 在任务明显匹配某技能描述时调用 `load_skill` 工具加载其全文再遵循，也可用 `/<name>` 直接加载。

内置技能（`skill/builtin/`，go:embed：`ratd` / `collab`）最先注册、优先级最低；用户技能从常见 agent 技能目录加载（`~/.claude/skills/`、`~/.agent/skills/`、`<项目>/.claude/skills/`、`~/.deepact/skills/`），同名冲突时 `~/.deepact/skills/` 覆盖。也可用 `skill_install` 工具从社区仓库安装技能。

格式为 `<name>/SKILL.md`（Claude Code 布局，YAML frontmatter）：

```markdown
---
name: my-flow
description: 处理模块 X 的代码审计；包含编译检查与测试生成。
when_to_use: 用户提到 X 相关代码时
next_skills: [writing-plans]
---
# My Workflow
1. 先做 A
2. 再做 B
3. 验证 C
```

### MCP 扩展

在 `.deepact/mcp.json`（或 `~/.deepact/mcp.json`）注册外部 MCP 服务器，其工具自动并入可用工具集，无需改代码。

```json
{
  "servers": [
    { "name": "github", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "env": { "GITHUB_TOKEN": "..." } }
  ]
}
```

## 🔌 对接 DeepSeek

DeepAct 从零为 DeepSeek 构建，不为"通用模型"妥协：

- **前缀缓存分层** —— 请求稳定区全命中、仅 volatile tail 缺失，直接省 token、降延迟。
- **`reasoning_content` 回显** —— DeepSeek 推理过程结构化存入会话，可回放、可审计。
- **温度分级路由** —— 依据任务类型（分析 / 编码 / 工具调用）自动调配温度，减少幻觉与无效重试。
- **双模型路由** —— 主循环的 `selectModel()` 固定返回主模型（稳定的 model 字段保 DeepSeek per-model 前缀缓存命中）；`flash` 仅用于子代理与压缩路径，性价比按需分配。
- **重试与限速** —— 按 DeepSeek 错误特征（限流 / 超时 / 截断）自动降级，不空转。

配置示例（`~/.deepact/config.toml` 或项目级 `.deepact/config.toml`）：

```toml
[model]
api_key = "sk-..."        # 或：DEEPSEEK_API_KEY 环境变量 / 首次启动 TUI 提示
default = "flash"         # 主循环使用的模型
escalation = "pro"        # 复杂任务使用的模型（可选）
reasoning_effort = "high" # 思考强度：none | low | high | max（可选，默认 high）

[search]
provider    = "tavily"    # 原生 web_search 工具
api_key     = "tvly-..."
max_results = 5
```

> [!TIP]
> 完整字段见配置文件内注释。模型与路由、上下文预算、UI、LSP、MCP 服务器、自定义子代理角色均可在 TOML 中配置。

## ✨ 核心能力

### 子代理并行

`handoff_to_agent` 委派给不同内置角色的子代理——`sub`（通用）、`researcher`（只读调研）、`critic`（对抗式审查），以及 `ratd` 流水线角色 `proposer` / `redteam` / `arbitrator`。同一轮发出的多个 handoff **并行**执行（`tools/registry.go` 每个工具调用一个 goroutine），结果汇聚回主循环——快而不乱。

### 可回退

每步操作写入不可变 JSONL：可回退到任意步骤、分叉新分支；工具输出内容寻址存储，落盘前自动脱敏密钥。

## 📦 CLI 命令一览

| 命令 | 说明 |
|------|------|
| `deepact` | 交互式 TUI（首次启动提示输入 API Key） |
| `deepact exec <prompt>` | 非交互 / CI 模式 |
| `deepact set api-key <key>` | 将 API Key 写入 `~/.deepact/config.toml` |
| `deepact eval history` / `stats` / `compare <v1> <v2>` | 提示版本评估与对比 |

## 🧱 架构

```text
cmd/      CLI 入口（Cobra）        ui/       终端 UI（Bubble Tea）
engine/   代理循环·子代理·共享类型中枢
context/  提示构建·目录树快照·压缩   llm/      DeepSeek 客户端（流式·重试·限速）
tools/    内置工具（builtin/）+ MCP（mcp/）   router/   模型路由
session/  JSONL 会话·分叉·回退       artifact/ 内容寻址存储·自动脱敏
skill/    内置（ratd/collab）+ 外部技能加载    config/   共享配置
memory/   跨会话持久记忆
```

分层：`engine/` 是共享类型/接口中枢（hub-and-spoke）。核心 `llm/`、`tools/` 文件零项目依赖，由小型 `adapter.go` 桥接文件对接 engine 类型；`engine/` 不依赖 `ui/`/`cmd/`；跨层调用走接口。

---

## License

[MIT](LICENSE)
