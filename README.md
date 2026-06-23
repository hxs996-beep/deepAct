# DeepAct — 专为 DeepSeek 打造的终端 AI 编码代理

<p align="center">
  <a href="https://github.com/hxs996-beep/deepAct/releases"><img src="https://img.shields.io/github/v/release/hxs996-beep/deepAct?style=flat-square" alt="Release"></a>
  <a href="https://goreportcard.com/report/github.com/deepact/deepact"><img src="https://goreportcard.com/badge/github.com/deepact/deepact?style=flat-square" alt="Go Report Card"></a>
  <a href="https://github.com/deepact/deepact/blob/main/LICENSE"><img src="https://img.shields.io/github/license/deepact/deepact?style=flat-square" alt="MIT License"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go" alt="Go 1.24+"></a>
</p>

<p align="center">
  <b>DeepSeek 模型深度适配 · 三闸防护 · ~98% 缓存命中率 · 技能系统 · 子代理并行</b>
  <br>
  <a href="#-快速开始">快速开始</a> ·
  <a href="#-功能总览">功能总览</a> ·
  <a href="#-用户接入点">用户接入点</a> ·
  <a href="#-技能系统">技能系统</a> ·
  <a href="#-配置指南">配置指南</a> ·
  <a href="#english">English</a>
</p>

---

> **DeepAct 是什么？** 一个运行在终端里的 AI 编码助手，专为 DeepSeek 模型（V4 Flash、R1）从零构建。与那些通用 AI 编码工具不同，DeepAct 的每一层——提示工程、缓存架构、温度调度、工具调用——都针对 DeepSeek API 的特性深度优化。这意味着：**更低的成本、更快的响应、更精准的指令遵循**。

---

## 🚀 快速开始

### 前置条件

一个 [DeepSeek API Key](https://platform.deepseek.com/)。二进制文件静态编译，零运行时依赖。

### 安装

**macOS（Homebrew）：**
```bash
brew install hxs996-beep/homebrew-tap/deepact
```

**Linux / macOS（一键脚本）：**
```bash
curl -sSfL https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.sh | sh
```

**Windows（PowerShell）：**
```powershell
powershell -c "irm https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.ps1 | iex"
```

**Go 安装（需要 Go 1.24+）：**
```bash
go install github.com/deepact/deepact@latest
```

**手动安装：** 从 [GitHub Releases](https://github.com/hxs996-beep/deepAct/releases) 下载对应平台的压缩包，解压后将二进制文件放入 `$PATH`。

### 配置 API Key

```bash
# 交互式配置（推荐）
deepact set api-key

# 或设置环境变量
export DEEPSEEK_API_KEY=sk-...
```

### 运行

```bash
# 交互式 TUI 模式（默认）
deepact

# 非交互 / CI 模式
deepact exec "修复连接池中的竞态条件"
```

---

## ✨ 功能总览

| 功能 | 说明 |
|------|------|
| **🎯 DeepSeek 原生适配** | 提示工程、前缀缓存、温度调度、`reasoning_content` 回显全部针对 DeepSeek API 调优 |
| **🛡️ 三闸防护系统** | 每步破坏性操作经过模糊检测、设计反模式审查、范围守卫三道关 |
| **🧠 方法学技能系统** | 内置 15+ 技能（头脑风暴、TDD、调试、代码审查等），可链式自动激活 |
| **🤖 子代理架构** | 复杂任务委派给专用子代理并行执行，结果汇聚回主循环 |
| **💾 会话分叉与回退** | 所有操作按 JSONL 不可变记录，可回退到任意步骤，或分叉出新分支 |
| **📦 内容寻址存储** | 工具输出通过 SHA256 去重，存储前自动脱敏 API Key 和密码 |
| **🔌 LSP 集成** | 利用 Language Server Protocol 获取精准的代码智能（跳转、类型查询、引用查找） |
| **⚡ ~98% 缓存命中率** | 分层提示构建，系统提示词稳定区完全命中缓存，仅 volatile tail 缺失 |
| **📋 跨平台** | macOS、Linux、Windows 全支持，静态编译，零依赖 |

---

## 🛡️ 三闸防护系统

每次执行破坏性操作（文件编辑、Shell 命令）前，DeepAct 都会经过三道闸门：

```
┌────────────────┐    ┌────────────────┐    ┌────────────────┐
│  Ambiguity     │ →  │  Design        │ →  │  Scope         │
│  Gate          │    │  Review Gate   │    │  Guard Gate    │
│                │    │                │    │                │
│ "请求是否清晰？" │    │ "方案是否有    │    │ "操作是否在    │
│                │    │  反模式？"     │    │  确认范围内？" │
└────────────────┘    └────────────────┘    └────────────────┘
        │                      │                      │
        ▼                      ▼                      ▼
    询问用户              审查并修正              拦截或确认
```

| 关卡 | 拦截什么 | 触发示例 |
|------|---------|---------|
| **模糊检测** | 含糊不清的请求 | "改进配置逻辑" → 问：改哪部分？怎么改？ |
| **设计审查** | 编码反模式 | 用文本内容当查找键、吞错误、过度实现 |
| **范围守卫** | 危险操作 | `rm -rf /`、修改已确认范围外的文件 |

---

## 🧠 技能系统

Skill 是 DeepAct 独有的方法学模板系统。每个 Skill 封装了一套完整的工作方法论，可通过 `/skillname` 激活。Skill 之间可以链式自动激活，形成完整的工作流。

### 内置 Skills

| Skill | 用途 | 后继 Skill |
|-------|------|------------|
| `brainstorming` | 需求探索与方案设计 | `writing-plans` |
| `writing-plans` | 生成结构化实现计划 | `executing-plans` |
| `executing-plans` | 按计划分步执行 | `finishing-a-development-branch` |
| `test-driven-development` | 先写测试，再实现 | — |
| `systematic-debugging` | 复现→隔离→修复→验证 | — |
| `code-review` | 系统化代码审查 | — |
| `requesting-code-review` | 请求他人审查代码 | — |
| `receiving-code-review` | 接收审查反馈后改进 | — |
| `subagent-driven-development` | 分解复杂任务为并行子代理 | — |
| `dispatching-parallel-agents` | 并行执行多个独立任务 | — |
| `verification-before-completion` | 完成前自动验证 | — |
| `using-superpowers` | 技能发现与激活引导 | — |
| `using-git-worktrees` | 隔离工作区开发 | — |
| `finishing-a-development-branch` | 完成后的合并/PR/清理决策 | — |
| `writing-skills` | 创建或编辑自定义 Skills | — |

### 链式工作流示例

```
/brainstorming  →  /writing-plans  →  /executing-plans  →  /finishing-a-development-branch
需求探索与设计     生成实现计划          分步执行               合并/PR/清理
```

只需输入 `/brainstorming`，完成后自动建议激活 `writing-plans`，用户确认后继续。

---

## 🔌 用户接入点

DeepAct 提供了多个扩展点，让用户可以根据项目需求定制行为。

### 1. `deepact.md` — 项目级规则文件

在项目根目录创建 `deepact.md`，定义本项目面向所有 AI 编码者的开发规范、编码标准和架构约束。DeepAct 启动时会自动加载此文件中的规则。

`deepact.md` 中可以定义：
- **项目标识**：名称、语言、架构
- **代码规范**：代码风格、命名规则、文件组织
- **架构规则**：依赖方向、接口边界、状态管理
- **设计原则**：先守卫后执行、结构化优于冗长等
- **测试要求**：覆盖目标、测试模式
- **安全规则**：敏感数据处理、Shell 执行白名单
- **Git 规范**：提交格式、分支命名、推送规则

> **模板参考**：DeepAct 项目自身的 `deepact.md` 就是一个完整的示例。

### 2. `.deepact/config.toml` — 项目级配置

在项目根目录创建 `.deepact/config.toml`（或全局 `~/.deepact/config.toml`）来自定义行为：

```toml
[model]
default = "deepseek-v4-flash"          # 默认模型
escalation = "deepseek-v4-flash"       # 风险升级模型
provider = "deepseek"                  # API 提供商

[routing]
risk_threshold = 0.55                  # 模型升级风险阈值
max_iterations = 15                    # 每轮最大工具迭代次数

[context]
max_budget_tokens = 131072             # 上下文预算上限
compact_every_n_turns = 5              # 每 N 轮强制压缩

[guards]
scope_guard = true                     # 开启范围守卫

[permissions]
mode = "ask"                           # auto / ask / deny
allow_shell = true                     # 允许 Shell 执行
dangerous_commands = [                 # 需要确认的危险命令
    "rm -rf", "git push --force", "sudo"
]

[ui]
theme = "auto"                         # 主题 auto / dark / light
show_thinking = false                  # 显示模型思考过程

[lsp]
enabled = true                         # 启用 LSP 集成
```

### 3. 自定义 Skills

你可以编写自己的 Skill，放入 `~/.deepact/skills/` 目录，DeepAct 会自动加载。每 个 Skill 是一个 TOML 文件：

```toml
# ~/.deepact/skills/my-workflow.toml
name = "my-workflow"
description = "我的自定义工作流程"
keywords = ["我的项目", "特定流程"]
next_skills = ["writing-plans"]

content = """
# My Custom Workflow

执行步骤：
1. 先做 A
2. 再做 B
3. 最后验证 C
"""
```

启动后在 TUI 中输入 `/my-workflow` 即可激活。

### 4. CLI 命令

| 命令 | 说明 |
|------|------|
| `deepact` | 启动交互式 TUI |
| `deepact exec "提示"` | 非交互模式（CI/脚本） |
| `deepact set api-key` | 配置 API Key |
| `deepact eval history` | 查看评估记录 |
| `deepact eval stats` | 查看评估统计 |
| `deepact eval compare <v1> <v2>` | 对比两个提示版本 |

### 5. 快捷键

| 按键 | 功能 |
|------|------|
| `Ctrl+Q` | 退出 |
| `Esc` | 取消当前任务 |
| `Enter` | 提交输入 |
| `Tab` | 自动补全 |
| `↑/↓` | 浏览建议 |
| `Alt+Enter` | 插入换行 |

---

## ⚙️ 配置指南

### 配置优先级

1. **环境变量**（`DEEPSEEK_API_KEY`、`DEEPACT_CONFIG`）
2. **项目配置**（`$PROJECT/.deepact/config.toml`）
3. **全局配置**（`~/.deepact/config.toml`）
4. **命令行参数**（`--model`、`--verbose`）

### 完整配置参考

所有可配置项见 `.deepact/config.toml` 中的注释说明。核心配置分类：

- **模型与路由**：默认模型、升级模型、API 提供商、推理模式
- **上下文管理**：Token 预算、代码片段大小、压缩频率
- **守卫系统**：范围守卫开关、工具迭代上限
- **权限控制**：Shell/文件操作权限、危险命令白名单
- **会话与存储**：会话目录、artifact 存储目录
- **UI 显示**：主题、思考过程、工具输出、路由决策
- **LSP 集成**：开关、自动检测、语言服务器配置

---

## 🏗️ 项目架构

```
cmd/          CLI 入口（Cobra）
ui/           终端 UI（Bubble Tea）
engine/       代理循环、守卫系统、子代理、评估
context/      提示构建、代码库映射、语言包、压缩
llm/          DeepSeek API 客户端（流式、重试、限速）
policy/       模糊检测、设计审查、范围守卫
tools/        内置工具（read/write/edit/search/bash/fetch/revert）
session/      JSONL 会话持久化、分叉与回退
artifact/     内容寻址存储、自动脱敏
router/       模型路由（可扩展）
skill/        方法学技能加载与注册
```

---

## 📊 对比其他工具

| 特性 | DeepAct | Aider | Cline (VS Code) |
|------|---------|-------|-----------------|
| **DeepSeek 原生优化** | ✅ 全面 | ⚠️ 通用 | ⚠️ 通用 |
| **缓存优化** | ✅ ~98% 命中率 | ❌ | ❌ |
| **三闸防护** | ✅ 3 层 | ❌ | ⚠️ 基础 |
| **技能系统** | ✅ 可链式激活 | ❌ | ❌ |
| **子代理并行** | ✅ | ❌ | ❌ |
| **会话分叉/回退** | ✅ 完整 | ❌ | ❌ |
| **终端原生** | ✅ TUI | ✅ CLI | ❌ 仅 VS Code |
| **开源免费** | ✅ MIT | ✅ Apache 2.0 | ✅ MIT |

---

## 📝 License

[MIT License](LICENSE)

---

<h1 id="english">English</h1>

<p align="center">
  <b>DeepAct — Terminal AI Coding Agent for DeepSeek Models</b><br>
  <i>Guarded execution · Instruction following · Context caching · Skills system</i>
</p>

DeepAct is a **terminal AI coding agent** purpose-built for **DeepSeek models** (V4 Flash, R1, and beyond). Unlike generic AI coding tools that wrap any LLM, DeepAct is engineered from the ground up for the DeepSeek API — leveraging its reasoning capabilities, prefix caching optimization, and temperature-graded routing to deliver precise, safe code modifications through a keyboard-driven CLI interface.

**Why DeepAct?** Existing AI coding assistants are optimized for GPT/Claude. DeepAct is the only agent that deeply understands DeepSeek's quirks: `reasoning_content` echo, cache architecture, tool-call formatting preferences. This means **lower cost, faster responses, and better instruction following** compared to using a generic agent with a DeepSeek model swap.

### Quick Start

```bash
# Install (macOS)
brew install hxs996-beep/homebrew-tap/deepact

# Configure API Key
deepact set api-key

# Run
deepact
```

### Key Features

- **DeepSeek-Native** — Prompt engineering, prefix caching, temperature scheduling, and `reasoning_content` management all tuned for DeepSeek API. ~98% cache hit rate.
- **Triple Guard Gates** — Ambiguity detection, anti-pattern design review, and scope enforcement before every destructive operation.
- **Methodology Skills System** — 15+ built-in skills (`/brainstorming`, `/test-driven-development`, `/systematic-debugging`, etc.). Skills can chain-automate: brainstorming → planning → execution → verification.
- **Sub-Agent Architecture** — Complex tasks decompose into parallel sub-agents that research independently and merge results back into the main loop.
- **Session Fork & Rewind** — Every interaction is an immutable JSONL log. Rewind to any step, fork a new branch, try a different approach.

### User Extension Points

**1. `deepact.md`** — Project-level rules file. Place in your project root to define code standards, architecture constraints, and design principles for AI agents.

**2. `.deepact/config.toml`** — Project-level configuration for model routing, permissions, UI, LSP, and more.

**3. Custom Skills** — Write your own TOML skill files in `~/.deepact/skills/` and activate them via `/<name>`.

### Installation

```bash
# macOS Homebrew
brew install hxs996-beep/homebrew-tap/deepact

# Linux/macOS one-liner
curl -sSfL https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.sh | sh

# Windows PowerShell
powershell -c "irm https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.ps1 | iex"

# Go install (requires Go 1.24+)
go install github.com/deepact/deepact@latest
```

### CLI Commands

| Command | Description |
|---------|-------------|
| `deepact` | Start interactive TUI |
| `deepact exec <prompt>` | Non-interactive / CI mode |
| `deepact set api-key` | Configure DeepSeek API Key |
| `deepact eval history` | View evaluation records |
| `deepact eval stats` | View evaluation statistics |
| `deepact eval compare <v1> <v2>` | Compare two prompt versions |

### License

MIT License
