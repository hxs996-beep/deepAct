# DeepAct — Lightweight Terminal AI Coding Agent · Built for DeepSeek

**English** | [简体中文](README.zh.md)

<p align="center">
  <a href="https://goreportcard.com/report/github.com/deepact/deepact"><img src="https://img.shields.io/badge/go_report-A-brightgreen?style=flat-square" alt="Go Report"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=flat-square" alt="MIT"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go" alt="Go 1.24+"></a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey?style=flat-square" alt="Platforms">
</p>

<p align="center">
  <b>⚡ Single binary · Zero runtime deps · DeepSeek-native</b>
</p>

**DeepAct is an AI coding agent that lives in your terminal** — written in Go, statically compiled, open source (MIT), and tuned end-to-end for the DeepSeek API. No Node, no Python, no Docker — just a binary and a DeepSeek API key.

---

## 📖 Contents

1. [Quick Start](#quick-start)
2. [Day-to-Day Use](#day-to-day-use)
3. [Connecting to DeepSeek](#connecting-to-deepseek)
4. [Core Capabilities](#core-capabilities)
5. [CLI Reference](#cli-reference)
6. [Architecture](#architecture)

---

## Quick Start

> [!NOTE]
> You need a [DeepSeek API Key](https://platform.deepseek.com/). On first launch, DeepAct prompts for it interactively in the TUI and persists it to `~/.deepact/config.toml` — no manual config step needed.

### Install

```bash
# macOS / Linux one-liner
curl -sSfL https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.sh | sh

# or Go
go install github.com/deepact/deepact@latest
```

Windows users: see [Releases](https://github.com/hxs996-beep/deepAct/releases) (PowerShell or manual download).

### Start Using

```bash
deepact                      # interactive TUI (Windows / macOS / Linux)
deepact exec "fix the connection-pool race"   # non-interactive / CI mode
```

The environment variable `DEEPSEEK_API_KEY` takes priority; a project-level `.deepact/config.toml` overrides the global config.

## Day-to-Day Use

### Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `Ctrl+Q` | Quit |
| `Esc` | Cancel current task / clear input |
| `Enter` | Submit |
| `Shift+Enter` | Newline |
| `Tab` | Complete |

### One-Line Tasks (from the shell)

```bash
deepact exec "add timeout and circuit breaker to LoginHandler"
deepact exec "migrate the user table to Postgres and fix all compile errors"
deepact exec "review the last 5 commits for potential bugs"
```

### Parallel Research (/collab)

```bash
deepact exec "/collab add a cache layer"
```

`/collab` is a built-in collaboration skill driven by `handoff_to_agent`: the main agent decomposes the goal into 2-6 research directions, then delegates each direction to a `researcher` sub-agent **in parallel** (read-only tools) and merges their findings into one structured report — faster than serial investigation when you need breadth quickly.

### Project Rules & Skills

Project conventions, workflows, and domain knowledge are injected into the system prompt via **skills**: the skill list is rendered into the stable zone, and the agent loads a skill's full instructions via the `load_skill` tool when your message names or clearly matches that skill. You can also load a skill directly with `/<name>`.

Built-in skills (`skill/builtin/`, go:embed: `ratd` / `collab`) load first at the lowest priority; user skills are loaded from common agent skill directories (`~/.claude/skills/`, `~/.agent/skills/`, `<project>/.claude/skills/`, `~/.deepact/skills/`), with `~/.deepact/skills/` winning on name conflicts. Skills are also installable from the community registry with `deepact`'s `skill_install` tool.

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

Register any MCP server in `.deepact/mcp.json` (or `~/.deepact/mcp.json`); its tools join the available tool set automatically, no code changes needed.

```json
{
  "servers": [
    { "name": "github", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "env": { "GITHUB_TOKEN": "..." } }
  ]
}
```

## Connecting to DeepSeek

DeepAct is built for DeepSeek from the ground up — it does not compromise for "generic models":

- **Layered prefix caching** — the stable region of a request is fully cache-hit, only the volatile tail misses, saving tokens and cutting latency.
- **`reasoning_content` echoes** — DeepSeek's reasoning is stored structurally in the session: replayable and auditable.
- **Tiered temperature routing** — temperature is tuned per task type (analysis / coding / tool calls), reducing hallucinations and wasted retries.
- **Dual-model routing** — the main loop's `selectModel()` always returns the configured primary model (a stable model field keeps DeepSeek's per-model prefix cache warm); `flash` is used only for sub-agents and the compaction path. Pay the right price per task.
- **Retry & rate limiting** — degrades gracefully on DeepSeek-specific error patterns (rate limits / timeouts / truncation) instead of spinning.

Config example (`~/.deepact/config.toml`, or project-level `.deepact/config.toml`):

```toml
[model]
api_key = "sk-..."        # or: DEEPSEEK_API_KEY env var / first-launch TUI prompt
default = "flash"         # model used for the main loop
escalation = "pro"        # model used for complex tasks (optional)
reasoning_effort = "high" # thinking effort: none | low | high | max (optional; default high)

[search]
provider    = "tavily"    # built-in web_search tool
api_key     = "tvly-..."
max_results = 5
```

> [!TIP]
> See the comments inside the config file for the full field list: model & routing, context budget, UI, LSP, MCP servers, and user-defined sub-agent roles are all TOML-configurable.

## Core Capabilities

### Parallel Subagents

`handoff_to_agent` delegates work to sub-agents with distinct built-in roles — `sub` (general), `researcher` (read-only investigation), `critic` (adversarial review), plus the `ratd` pipeline roles `proposer` / `redteam` / `arbitrator`. Multiple handoffs issued in one turn run **in parallel** (`tools/registry.go` spawns one goroutine per tool call), with results merged back into the main loop — fast without getting messy.

### Rewindable Sessions

Every step is written to an immutable JSONL log: rewind to any step, fork a new branch; tool output is content-addressed and secrets are auto-redacted before hitting disk.

## CLI Reference

| Command | Description |
|---------|-------------|
| `deepact` | Interactive TUI (first launch prompts for the API key) |
| `deepact exec <prompt>` | Non-interactive / CI mode |
| `deepact set api-key <key>` | Store the API key in `~/.deepact/config.toml` |
| `deepact eval history` / `stats` / `compare <v1> <v2>` | Prompt-version evaluation and comparison |

## Architecture

```text
cmd/      CLI entry (Cobra)         ui/       Terminal UI (Bubble Tea)
engine/   agent loop · subagents · shared type hub
context/  prompt build · tree snapshot · compaction   llm/      DeepSeek client (stream·retry·rate)
tools/    built-in tools (builtin/) + MCP (mcp/)      router/    model routing
session/  JSONL sessions·fork·rewind  artifact/ content-addressed store·auto-redact
skill/    built-in (ratd/collab) + external skill loading    config/    shared config
memory/   cross-session persistent memory
```

Layering: `engine/` is the shared type/interface hub (hub-and-spoke). Core `llm/` and `tools/` files have zero project imports; small `adapter.go` files bridge them to engine types. `engine/` never imports `ui/`/`cmd/`; cross-layer calls go through interfaces.

---

## License

[MIT](LICENSE)
