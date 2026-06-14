# DeepAct — AI Coding Agent for DeepSeek

<p align="center">
  <a href="https://github.com/hxs996-beep/deepAct/releases"><img src="https://img.shields.io/github/v/release/hxs996-beep/deepAct?style=flat-square" alt="Release"></a>
  <a href="https://goreportcard.com/report/github.com/deepact/deepact"><img src="https://goreportcard.com/badge/github.com/deepact/deepact?style=flat-square" alt="Go Report Card"></a>
  <a href="https://github.com/deepact/deepact/blob/main/LICENSE"><img src="https://img.shields.io/github/license/deepact/deepact?style=flat-square" alt="MIT License"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go" alt="Go 1.24+"></a>
</p>

<p align="center">
  <b>A terminal-native AI coding agent optimized for DeepSeek models</b><br>
  <i>Guarded execution · Instruction following · Context caching · Skills system</i>
  <br><br>
  <a href="#-quick-start">Quick Start</a> ·
  <a href="#-features">Features</a> ·
  <a href="#-keyboard-shortcuts">Shortcuts</a> ·
  <a href="#-comparison">vs Other Tools</a>
</p>

---

> **🇨🇳 中文用户**: DeepAct 是为 DeepSeek 模型深度定制的终端 AI 编码代理。内置指令遵循增强、三闸防护系统、多层提示词缓存，让 DeepSeek 在终端编码场景下发挥最大能力。[查看中文说明](#-chinese-version)

---

DeepAct is a **terminal AI coding agent** purpose-built for **DeepSeek models** (V4 Flash, R1, and beyond). Unlike generic AI coding tools that wrap any LLM, DeepAct is engineered from the ground up for the DeepSeek API — leveraging its reasoning capabilities, prefix caching optimization, and temperature-graded routing to deliver precise, safe code modifications through a keyboard-driven CLI interface.

**Why DeepAct?** Existing AI coding assistants (GitHub Copilot, Cursor, Aider) are optimized for GPT/Claude. DeepAct is the only agent that deeply understands DeepSeek's quirks: `reasoning_content` echo, cache architecture, tool-call formatting preferences. This means **lower cost, faster responses, and better instruction following** compared to using a generic agent with a DeepSeek model swap.

## ✨ Features

- **🎯 DeepSeek-Native** — Prompt engineering, prefix caching, temperature scheduling, and `reasoning_content` management all tuned specifically for DeepSeek API behavior. ~98% cache hit rate means dramatically lower API costs.
- **🛡️ Triple Guard Gates** — Ambiguity detection, anti-pattern design review, and scope enforcement. Every code modification passes through three safety checks before execution.
- **🧠 Methodology Skills System** — Built-in skill library (`/brainstorming`, `/test-driven-development`, `/systematic-debugging`, `/code-review`). Skills chain-automate: brainstorming flows into planning, planning into execution, execution into verification.
- **🤖 Sub-Agent Architecture** — Complex tasks decompose into parallel sub-agents. Each sub-agent researches independently; results merge back into the main loop. Think of it as a coding roundtable.
- **⏪ Session Fork & Rewind** — Every interaction is an immutable JSONL log. Rewind to any step, fork a new branch, try a different approach. No more "undo fear."
- **📦 Content-Addressed Artifact Store** — Tool outputs deduplicated by SHA256. Automatic redaction of API keys, passwords, and secrets before storage.

## 🚀 Quick Start

### Prerequisites

A [DeepSeek API Key](https://platform.deepseek.com/). Binaries are statically compiled — zero runtime dependencies.

### Installation

**macOS (Homebrew):**
```bash
brew install hxs996-beep/homebrew-tap/deepact
```

**Linux / macOS (one-liner):**
```bash
curl -sSfL https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.sh | sh
```

**Windows (PowerShell):**
```powershell
powershell -c "irm https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.ps1 | iex"
```

**Go (requires Go 1.24+):**
```bash
go install github.com/deepact/deepact@latest
```

**Manual:** Download from [GitHub Releases](https://github.com/hxs996-beep/deepAct/releases), extract, and place the binary in your `$PATH`.

### Configure API Key

```bash
deepact set api-key
```

Or set the environment variable:

```bash
export DEEPSEEK_API_KEY=sk-...
```

### Run

```bash
# Interactive TUI mode (default)
deepact

# Non-interactive / CI mode
deepact exec "Fix the race condition in the connection pool"
```

## 🔧 Core Mechanics

### Triple Guard Gates

Before every destructive operation, DeepAct runs three checks:

```
┌──────────────┐    ┌──────────────┐    ┌──────────────┐
│  Ambiguity   │ →  │  Design      │ →  │  Scope       │
│  Gate        │    │  Review Gate │    │  Guard Gate  │
│              │    │              │    │              │
│ "Is the      │    │ "Does the    │    │ "Is the      │
│  request     │    │  plan have   │    │  operation   │
│  clear?"     │    │  anti-       │    │  within      │
│              │    │  patterns?"  │    │  scope?"     │
└──────────────┘    └──────────────┘    └──────────────┘
        │                   │                   │
        ▼                   ▼                   ▼
    Ask user           Review & fix        Block or confirm
```

| Gate | What It Blocks | Trigger Example |
|------|---------------|-----------------|
| **Ambiguity** | Vague requests | "Improve the config handling" — asks: which part? how? |
| **Design Review** | Anti-patterns | Using display text as lookup key, swallowing errors, over-implementation |
| **Scope Guard** | Unsafe operations | `rm -rf /`, editing files outside confirmed scope |

### Prompt Cache Architecture

DeepAct's layered prompt construction achieves ~98% prefix cache hit rates:

```
[STABLE ZONE — always cached]
  Message 1: System prompt (never changes)
  Message 2: Session environment (detected once at startup)
  Message 3: Available skills (built once at startup)
  Message 4: RepoMap (stable within a session)

[HISTORY ZONE — appended, mostly cached]
  Multi-turn conversation history

[VOLATILE TAIL — only ~500-1000 tokens miss cache]
  AccumulatedBlocks + TaskState + TaskReminder
```

### Methodology Skills

Activate with `/<name>`. Skills can chain-automate:

```
/brainstorming → /writing-plans → /executing-plans → /finishing-a-development-branch
```

Available skills:

| Skill | Purpose |
|-------|---------|
| `brainstorming` | Explore requirements, discuss design before coding |
| `writing-plans` | Generate structured implementation plans |
| `test-driven-development` | Write tests first, then implement |
| `systematic-debugging` | Replicate → isolate → fix → verify |
| `code-review` | Systematic checklist-based code review |
| `subagent-driven-development` | Decompose complex tasks into parallel sub-agents |
| `verification-before-completion` | Auto-verify before claiming completion |

### Session Persistence

All interactions stored as JSONL in `~/.deepact/sessions/`. Supports:

- **Rewind**: Re-execute from any point in time
- **Fork**: Create branches from any step, explore alternative approaches
- **Audit**: Complete history with every tool call timestamped

## ⌨️ Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `Ctrl+Q` | Exit |
| `Esc` | Cancel running task |
| `Enter` | Submit input |
| `Tab` | Autocomplete |
| `↑/↓` | Browse suggestions |
| `Alt+Enter` | Insert newline |
| `Shift+drag` | Select text (bypasses mouse scroll mode) |

## 📋 CLI Commands

| Command | Description |
|---------|-------------|
| `deepact` | Start interactive TUI |
| `deepact exec <prompt>` | Non-interactive mode |
| `deepact eval history` | View evaluation records |
| `deepact eval stats` | View evaluation statistics |
| `deepact eval compare <v1> <v2>` | Compare two prompt versions |
| `deepact set api-key` | Configure DeepSeek API Key |

## 🏗️ Architecture

```
cmd/          CLI entry (Cobra)
ui/           Terminal UI (Bubble Tea)
engine/       Agent loop, guard system, sub-agents, evaluation
context/      Prompt construction, repo map, language packs, compression
llm/          DeepSeek API client (streaming, retry, rate limit, echo)
policy/       Ambiguity detection, design review, scope guarding
tools/        Built-in tools (read, write, edit, search, bash, fetch, revert)
session/      JSONL session persistence, fork & rewind
artifact/     Content-addressed storage with automatic redaction
router/       Model routing (extensible)
skill/        Methodology skill loading & registration (TOML-defined)
```

## 📊 Comparison

| Feature | DeepAct | Aider | Cline (VS Code) |
|---------|---------|-------|-----------------|
| **DeepSeek-native** | ✅ Fully optimized | ⚠️ Generic | ⚠️ Generic |
| **Cache optimization** | ✅ ~98% hit rate | ❌ No prefix caching | ❌ No prefix caching |
| **Guard gates** | ✅ 3-tier | ❌ None | ⚠️ Basic |
| **Skills system** | ✅ Chainable | ❌ | ❌ |
| **Sub-agents** | ✅ Parallel | ❌ | ❌ |
| **Session fork/rewind** | ✅ Full | ❌ | ❌ |
| **Terminal-native** | ✅ TUI | ✅ CLI | ❌ VS Code only |
| **Free** | ✅ MIT | ✅ Apache 2.0 | ✅ MIT |

## ⚙️ Configuration

Create `~/.deepact/config.toml`:

```toml
[model]
default = "deepseek-v4-flash"

[context]
max_budget_tokens = 1048576

[guards]
scope_guard = true
```

## 📄 License

[MIT License](LICENSE)

---

## 🇨🇳 Chinese Version

<p align="center">
  <b>DeepAct — DeepSeek 原生适配的 AI 编码助手</b><br>
  <i>指令遵循增强 · 三闸防护 · 多层压缩与缓存优化 · 方法学技能系统</i>
</p>

DeepAct 是一个专为 **DeepSeek 模型深度定制** 的终端 AI 编码代理。它利用 DeepSeek 的推理能力，同时通过三层防护机制确保代码修改精确、安全、高效。

**核心特性：**

- **原生 DeepSeek 适配** — 提示词工程、缓存优化、温度分级调度全部针对 DeepSeek API 特性调优。`reasoning_content` 回显、前缀缓存优化、流式实时输出，开箱即用。
- **三闸防护系统** — 模糊请求拦截、设计反模式审查、操作范围守卫。每次破坏性操作都经过三道闸门检查。
- **多层压缩与缓存覆盖** — 系统提示词稳定区 + 累积历史前缀缓存 + 仅 volatile tail 缺失。缓存命中率约 98%，大幅降低 API 开销。
- **Methodology Skills** — 内置方法学技能库（brainstorming、TDD、systematic-debugging）。技能可链式自动激活。
- **子代理系统** — 复杂任务可委派给专用子代理并行执行，结果汇聚回主循环。
- **Session 分叉与回退** — 所有操作按 JSONL 逐条记录，永不篡改。可回退到任意步骤，或分叉出新分支尝试不同方案。

**快速安装：**

```bash
# macOS Homebrew
brew install hxs996-beep/homebrew-tap/deepact

# Linux/macOS 一键脚本
curl -sSfL https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.sh | sh

# Windows PowerShell
powershell -c "irm https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.ps1 | iex"
```

**配置 API Key：**
```bash
deepact set api-key
```

**运行：**
```bash
deepact                    # 交互式 TUI 模式
deepact exec "修复xxx"     # 非交互模式
```

更多详情请参考上方英文文档。完整功能一致。
