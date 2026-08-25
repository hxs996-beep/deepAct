# DeepAct — Lightweight Terminal AI Coding Agent · Built for DeepSeek

**English** | [简体中文](README.zh.md)

<p align="center">
  <a href="https://goreportcard.com/report/github.com/deepact/deepact"><img src="https://img.shields.io/badge/go_report-A-brightgreen?style=flat-square" alt="Go Report"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=flat-square" alt="MIT"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go" alt="Go 1.24+"></a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey?style=flat-square" alt="Platforms">
</p>

<p align="center">
  <b>⚡ Single binary · ~6 MB download · ~25 MB memory · Zero runtime deps · DeepSeek-native</b>
</p>

**DeepAct is an AI coding agent that lives in your terminal** — written in Go, statically compiled, open source (MIT), and tuned end-to-end for the DeepSeek API.

- **Light** — a ~6 MB download. No Node, no Python, no Docker.
- **Fast** — prompt engineering, prefix caching, temperature scheduling, and tool-call formats are all tailored to DeepSeek.
- **Accurate** — compared to "a generic agent pointed at DeepSeek," it is **cheaper, faster, and follows instructions more precisely**.

---

## 📖 Contents

1. [How Lightweight It Is](#how-lightweight-it-is)
2. [Quick Start](#quick-start)
3. [Day-to-Day Use](#day-to-day-use)
4. [Connecting to DeepSeek](#connecting-to-deepseek)
5. [Core Capabilities](#core-capabilities)
6. [CLI Reference](#cli-reference)
7. [Architecture](#architecture)

---

## How Lightweight It Is

| Metric | Measured | Notes |
|--------|----------|-------|
| Download size | **~6 MB** (tar.gz) | Linux / macOS / Windows, amd64 + arm64 |
| Single binary | **~16 MB** | Static build (`CGO_ENABLED=0` + `-s -w`), zero external libraries |
| Peak startup memory | **~25 MB** | Measured with `deepact --help` (macOS arm64) |
| Startup time | **~10 ms** | Same measurement |
| Runtime dependencies | **0** | No Node / Python / Docker / Electron — just a DeepSeek API key |

*Measured on release 1.0.6 (macOS arm64); figures vary slightly by platform.*

One 16 MB Go file that ships a full agent: four guards, team collaboration, parallel subagents, MCP extension, and rewindable sessions. No browser kernel, no runtime baggage — **launch and go**; it runs happily on servers, CI runners, and low-end laptops.

## Quick Start

> [!NOTE]
> You need a [DeepSeek API Key](https://platform.deepseek.com/) (sign up at [platform.deepseek.com](https://platform.deepseek.com/)).

### Step 1 · Install

```bash
# macOS / Linux one-liner
curl -sSfL https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.sh | sh

# or Homebrew
brew install hxs996-beep/homebrew-tap/deepact

# or Go
go install github.com/deepact/deepact@latest
```

Windows users: see [Releases](https://github.com/hxs996-beep/deepAct/releases) (PowerShell or manual download).

### Step 2 · Configure Your DeepSeek API Key

```bash
deepact set api-key          # interactive; writes ~/.deepact/config.toml (mode 0600)
```

> [!TIP]
> A project-level `.deepact/config.toml` overrides the global config, so different repos can use different models and permission modes.

### Step 3 · Start Using

```bash
deepact                      # interactive TUI (Windows / macOS / Linux)
deepact exec "fix the connection-pool race"   # non-interactive / CI mode
deepact --auto exec "..."    # auto mode (skip confirmations)
deepact --model pro "..."    # pick a model: flash (fast/cheap) or pro (strong/full)
```

## Day-to-Day Use

### Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `Ctrl+Q` | Quit |
| `Esc` | Cancel current task |
| `Enter` | Submit |
| `Tab` | Complete |
| `Alt+Enter` | Newline |

### One-Line Tasks (from the shell)

```bash
deepact exec "add timeout and circuit breaker to LoginHandler"
deepact exec "migrate the user table to Postgres and fix all compile errors" --auto
deepact exec "review the last 5 commits for potential bugs" --output jsonl > review.jsonl
```

Common `exec` flags: `--auto` skip confirmations · `--output human|jsonl` · `--max-turns N` · `--model flash|pro` · `--verbose`.

### Multi-Agent Team Mode (/team)

```bash
deepact exec "/team add idempotency control to the order module"
```

The main agent first produces 2–3 implementation plans; then roles like architect and security engineer **review in parallel and score independently**, producing a plan × role score matrix. Once you pick a plan, the agent lands it directly. Supports `--members` for custom roles and `--add` to load TOML role files.

### Project Rules & Skills

Project conventions, workflows, and domain knowledge are injected into the system prompt via **skills**: the skill list is rendered into the stable zone, and the agent **auto-activates** the most relevant skill by semantically matching your message against each skill's `name`/`description` (silently falls back on mismatch). You can also switch manually with the `activate_skill` tool.

Skill directories are loaded by priority (later ones win on name conflicts):

| Priority | Directory | Notes |
|----------|-----------|-------|
| 1 | `~/.deepact/skills/` | DeepAct-specific |
| 2 | `<project>/.claude/skills/` | Project-level |
| 3 | `~/.agent/skills/` | Agent-generic |
| 4 | `~/.claude/skills/` | Claude Code compatible |

Format: `<name>/SKILL.md` (Claude Code layout, YAML frontmatter):

```markdown
---
name: my-flow
description: Audits code in module X; includes compile checks and test generation.
when_to_use: When the user mentions code related to X
next_skills: [writing-plans]
---
# My Workflow
1. Do A
2. Do B
3. Verify C
```

### MCP Support

Register any MCP server in the `[mcp]` section of `config.toml`; its tools join the available tool set automatically, no code changes needed.

## Connecting to DeepSeek

DeepAct is built for DeepSeek from the ground up — it does not compromise for "generic models":

- **Layered prefix caching** — the stable region of a request is fully cache-hit, only the volatile tail misses, saving tokens and cutting latency.
- **`reasoning_content` echoes** — DeepSeek's reasoning is stored structurally in the session: replayable and auditable.
- **Tiered temperature routing** — temperature is tuned per task type (analysis / coding / tool calls), reducing hallucinations and wasted retries.
- **Dual-model routing** — `flash` (fast, cheap) handles tool calls and routine work; `pro` (strong) handles design review and hard reasoning. Pay the right price per task.
- **Retry & rate limiting** — degrades gracefully on DeepSeek-specific error patterns (rate limits / timeouts / truncation) instead of spinning.

Config example (`~/.deepact/config.toml`, or project-level `.deepact/config.toml`):

```toml
[model]
api_key = "sk-..."        # or: deepact set api-key
default = "flash"         # default routing model

[search]
provider    = "tavily"    # built-in web_search tool
api_key     = "tvly-..."
max_results = 5
```

> [!TIP]
> See the comments inside the config file for the full field list: model & routing, permission modes, context budget, UI, LSP, and MCP servers are all TOML-configurable.

## Core Capabilities

### The Four Guards

Every destructive action (file edits, shell commands) passes four gates:

1. **Ambiguity Check** — vague requests get questioned back
2. **Design Review** — anti-pattern plans get rejected
3. **Scope Guard** — out-of-scope actions get blocked
4. **Loop Detection** — spinning in circles gets stopped

### Parallel Subagents

Complex tasks are split across dedicated subagents (searcher / planner / critic / tester) that run independently, with results merged back into the main loop — fast without getting messy.

### Rewindable Sessions

Every step is written to an immutable JSONL log: rewind to any step, fork a new branch; tool output is content-addressed and secrets are auto-redacted before hitting disk.

## CLI Reference

| Command | Description |
|---------|-------------|
| `deepact` | Interactive TUI |
| `deepact exec <prompt>` | Non-interactive / CI mode (`--auto`, `--output`, `--max-turns`) |
| `deepact set [key] [value]` | Config entries (e.g. `set api-key`) |
| `deepact eval history` / `stats` / `compare <v1> <v2>` | Prompt-version evaluation and comparison |

## Architecture

```text
cmd/      CLI entry (Cobra)         ui/       Terminal UI (Bubble Tea)
engine/   agent loop·guards·roundtable·subagents   policy/   ambiguity·design·scope guards
context/  prompt build·tree snapshot·compaction   llm/      DeepSeek client (stream·retry·rate)
tools/    built-in tools + MCP      router/    model routing
session/  JSONL sessions·fork·rewind  artifact/ content-addressed store·auto-redact
skill/    external skill loading    config/    shared config
```

Layering rules: `engine/` never imports `ui/`/`cmd/`; `tools/` never imports `engine/`; cross-layer calls go through interfaces.

---

## License

[MIT](LICENSE)
