<p align="center">
  <a href="https://github.com/hxs996-beep/deepAct/releases"><img src="https://img.shields.io/github/v/release/hxs996-beep/deepAct?style=flat-square" alt="Release"></a>
  <a href="https://goreportcard.com/report/github.com/hxs996-beep/deepAct"><img src="https://goreportcard.com/badge/github.com/hxs996-beep/deepAct?style=flat-square" alt="Go Report Card"></a>
  <a href="https://github.com/hxs996-beep/deepAct/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=flat-square" alt="MIT License"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go" alt="Go 1.24+"></a>
</p>

# DeepAct

> **终端里的 AI 编码代理，专为 DeepSeek 模型从零构建。**
>
> 与通用 AI 编码工具不同，DeepAct 的每一层——提示工程、缓存架构、温度调度、工具调用——都针对 DeepSeek API 深度优化。结果：**更低的 API 成本、~98% 前缀缓存命中率、更精准的指令遵循。**

---

## 为什么选 DeepAct

**问题：** 市面上的 AI 编码工具都是为 GPT/Claude 优化的。换一个 DeepSeek 模型上去，缓存策略失效、工具调用格式错位、`reasoning_content` 被丢弃——你付了 API 费却没拿到该有的效果。

**DeepAct 从三个层面解决这个问题：**

| 层面 | 做法 | 收益 |
|------|------|------|
| **提示工程** | 系统提示分层构建，稳定部分命中前缀缓存，仅 volatile tail 每次重建 | ~98% 缓存命中率，token 消耗锐减 |
| **安全执行** | 每次破坏性操作经过三道闸门：模糊检测 → 设计审查 → 范围守卫 | 不会因为一句含糊指令删错文件 |

---

## 核心能力

| 能力 | 一句话说明 |
|------|-----------|
| 🛡️ **三闸防护** | 编辑/Shell 前自动检测歧义、反模式、越权——三道关，缺一不可 |
| 🤖 **子代理并行** | 复杂任务自动拆解为独立子代理并行执行，结果汇聚回主循环 |
| 👥 **团队协作** | `/team` 激活架构师/安全/质量/维护 4 角色并行分析，经评审→证伪→合成输出 |
| 💾 **会话分叉与回退** | 所有操作 JSONL 不可变记录，可回退到任意步骤或分叉新分支 |
| 📦 **内容寻址存储** | 工具输出 SHA256 去重，存储前自动脱敏 API Key 和密码 |
| 🔌 **LSP 集成** | 利用 Language Server Protocol 实现精准代码智能——跳转定义、类型查询、引用查找 |
| 📋 **跨平台** | macOS / Linux / Windows 全支持，Go 静态编译，零运行时依赖 |

---

## 安装

```bash
# macOS Homebrew
brew install hxs996-beep/homebrew-tap/deepact

# Linux / macOS 一键脚本
curl -sSfL https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.sh | sh

# Windows PowerShell
powershell -c "irm https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.ps1 | iex"

# Go install（需要 Go 1.24+）
go install github.com/deepact/deepact@latest
```

也可从 [GitHub Releases](https://github.com/hxs996-beep/deepAct/releases) 下载对应平台的二进制文件。

---

## 用户扩展点

### `deepact.md` — 项目级规则

在项目根目录放置 `deepact.md`，定义编码规范、架构约束、设计原则。DeepAct 启动时自动加载。支持按语言包定制（Go、Python、Rust、TypeScript 等）。

### `.deepact/config.toml` — 项目级配置

```toml
[model]
api_key = "sk-..."          # 或通过环境变量 DEEPSEEK_API_KEY
default = "deepseek-v4-flash"

[permissions]
mode = "ask"                # auto | ask | deny
dangerous_commands = ["rm -rf", "git push --force", "sudo"]

[context]
max_budget_tokens = 131072
```

完整配置项见 `.deepact/config.toml` 内注释。

### 自定义 Skills

将 TOML 文件放入 `~/.deepact/skills/`，通过 `/<name>` 激活：

```toml
# ~/.deepact/skills/my-workflow.toml
name = "my-workflow"
description = "我的自定义工作流"
next_skills = ["writing-plans"]

content = """
# My Workflow
1. 先做 A
2. 再做 B
3. 最后验证 C
"""
```

---

## 三闸防护系统

```
┌──────────────────┐     ┌──────────────────┐     ┌──────────────────┐
│  Ambiguity Gate  │ ──▶ │  Design Review   │ ──▶ │  Scope Guard     │
│  请求是否清晰？    │     │  方案是否有反模式？ │     │  操作是否在范围内？ │
└──────────────────┘     └──────────────────┘     └──────────────────┘
        │                          │                          │
        ▼                          ▼                          ▼
    询问用户                   审查并修正                  拦截或确认
```

| 关卡 | 拦截场景 | 示例 |
|------|---------|------|
| 模糊检测 | 指令含糊不清 | "改进配置逻辑" → 追问：改哪部分？怎么改？ |
| 设计审查 | 编码反模式 | 用文本内容当查找键、吞错误、过度实现 |
| 范围守卫 | 越权操作 | `rm -rf /`、修改已确认范围外的文件 |

---

## CLI 命令

| 命令 | 说明 |
|------|------|
| `deepact` | 启动交互式 TUI |
| `deepact exec "<提示>"` | 非交互模式（CI / 脚本） |
| `/team <需求>` | 多角色团队协作（TUI 内） |
| `deepact set api-key` | 配置 API Key |
| `deepact eval history` | 查看评估记录 |
| `deepact eval stats` | 查看评估统计 |
| `deepact eval compare <v1> <v2>` | 对比两个提示版本 |

---

## 项目架构

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

## License

[MIT](LICENSE)

---

# English

<p align="center">
  <b>DeepAct — Terminal AI Coding Agent for DeepSeek Models</b><br>
  <i>Guarded execution · ~98% cache hit rate · Skills system · Sub-agent parallelism</i>
</p>

DeepAct is a **terminal AI coding agent** purpose-built for **DeepSeek models** (V4 Flash, R1). Unlike generic AI coding tools that wrap any LLM, DeepAct is engineered from the ground up for the DeepSeek API — leveraging its reasoning capabilities, prefix caching optimization, and temperature-graded routing to deliver precise, safe code modifications.

### Why DeepAct

Existing AI coding assistants are optimized for GPT/Claude. Swap in a DeepSeek model and you lose cache efficiency, tool-call formatting breaks, and `reasoning_content` gets discarded. DeepAct solves this at three levels:

| Layer | Approach | Result |
|-------|----------|--------|
| **Prompt Engineering** | Layered prompt construction — stable sections hit prefix cache, only volatile tail rebuilds | ~98% cache hit rate, drastically lower token cost |
| **Safety** | Triple-gate system: ambiguity detection → design review → scope enforcement before every destructive operation | No accidental damage from vague instructions |

### Quick Start

```bash
brew install hxs996-beep/homebrew-tap/deepact
deepact set api-key
deepact exec "find the entry point in cmd/ and explain the startup flow"
```

### Core Capabilities

- 🛡️ **Triple-Gate Guard** — Ambiguity detection, anti-pattern review, and scope enforcement before every edit or shell command
- 🤖 **Sub-Agent Parallelism** — Complex tasks auto-decompose into independent sub-agents executing in parallel
- 👥 **Team Collaboration** — `/team` activates 4 expert roles (architect, security, quality, maintainer) for parallel analysis → review → refutation → synthesis
- 💾 **Session Fork & Rewind** — Immutable JSONL audit log; rewind to any step or fork a new branch
- 📦 **Content-Addressed Storage** — SHA256-deduplicated tool outputs with automatic API key/password redaction
- 🔌 **LSP Integration** — Go-to-definition, type queries, and reference lookups via Language Server Protocol
- 📋 **Cross-Platform** — macOS, Linux, Windows; statically compiled Go binary, zero runtime dependencies

### Extension Points

**`deepact.md`** — Project-level rules file defining code standards, architecture constraints, and design principles. Auto-loaded on startup. Language packs available for Go, Python, Rust, TypeScript, and more.

**`.deepact/config.toml`** — Per-project configuration for model routing, permissions, context budget, and UI preferences.

**Custom Skills** — Drop TOML files into `~/.deepact/skills/` and activate via `/<name>`.

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
| `deepact exec "<prompt>"` | Non-interactive / CI mode |
| `/team <goal>` | Team collaboration mode (in TUI) |
| `deepact set api-key` | Configure API Key |
| `deepact eval history` | View evaluation records |
| `deepact eval stats` | View evaluation statistics |
| `deepact eval compare <v1> <v2>` | Compare two prompt versions |

### License

[MIT](LICENSE)
