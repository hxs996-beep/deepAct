# DeepAct

A terminal-native AI coding agent with **guarded execution** and **dual-model routing**. DeepAct explores your codebase, proposes approaches, implements changes, reviews the results — and never acts without guardrails.

## Why DeepAct?

Most AI coding agents are a single loop: listen → act → repeat. DeepAct is different:

- **Three guard gates** intercept every destructive action — is the request vague? Is the design flawed? Is the operation in scope?
- **Dual-model routing** uses a fast cheap model for routine tasks and escalates to a powerful model when complexity demands it — cutting cost by ~80% on typical sessions.
- **Session fork/rewind** lets you branch from any point in history or roll back without losing context.
- **Every task is scored** — a structured scorecard tracks design quality, so you see how well the agent performs over time.

## How It Works

```
┌──────────────┐    ┌──────────────┐    ┌──────────────┐
│  Ambiguity   │ →  │   Design     │ →  │  Execution   │
│  Gate        │    │   Guard      │    │  + Scope     │
│              │    │              │    │  Guard       │
│ "Is this     │    │ "Is the      │    │ "Is this op  │
│  request     │    │  plan robust │    │  in scope?"  │
│  clear?"     │    │  or lazy?"   │    │              │
└──────────────┘    └──────────────┘    └──────────────┘
        │                   │                   │
        ▼                   ▼                   ▼
   Ask user          Review & revise       Block or ask
```

DeepAct doesn't just run a single agent loop — it runs a **pipeline with guard gates**:

1. **Ambiguity Gate** — detects vague requests and asks clarifying questions before any code change
2. **Design Guard** — reviews the agent's plan against known anti-patterns (over-implementation, lazy design, fragile lookups)
3. **Scope Guard** — blocks destructive operations (file edits, bash commands) unless explicitly confirmed
4. **Loop Guard** — detects repeated tool calls on the same file without progress

### Conference Pipeline

For complex tasks, DeepAct runs a structured pipeline:

| Phase | What happens |
|---|---|
| `/plan <goal>` | Explore codebase → brainstorm proposals → challenge each proposal → you pick one |
| `/implement <goal>` | Execute the plan step by step with scope guard watching |
| `/review` | Scorecard compares implementation vs plan, flags drift |

Or just describe what you want — DeepAct auto-detects complexity and enters the pipeline when needed.

## Guard System

Three layers protect your project from bad AI decisions:

| Guard | What it blocks | How |
|---|---|---|
| **Ambiguity Gate** | "Improve the config handling" (too vague) | Asks: *What config? What improvement?* |
| **Design Guard** | Using display text as lookup key, swallowing errors | Reviews plan against 10+ anti-patterns |
| **Scope Guard** | `rm -rf /`, edit outside working set | User must confirm before execution |

## Dual-Model Routing

Not every turn needs the full model. DeepAct uses **two models**:

| Model | Used for | Cost |
|---|---|---|
| **Flash** (`deepseek-v4-flash`) | Simple read/glorify/search turns | ~¥1/M input tokens |
| **Pro** (`deepseek-v4-pro`) | Planning, design review, complex edits | ~¥3/M input tokens |

The router decides which model to use based on ambiguity, edit scope size, and failure history.

## Session Fork & Rewind

All session history is stored as append-only JSONL — never deleted, never modified.

```
deepact exec "fix the nil pointer"    # Session A
# Oops, went too far...
deepact exec --rewind 3              # Rewind to step 3

# Want to try a different approach?
deepact exec --fork                   # Branch from current state
```

Every step is replayable. No data loss.

## Quick Start

### Prerequisites

A [DeepSeek API key](https://platform.deepseek.com/). No other runtime dependencies — binaries are static.

### Install (choose one)

**macOS (Homebrew):**
```bash
brew install deepact/tap/deepact
```

**Linux / macOS (curl install):**
```bash
curl -sSfL https://raw.githubusercontent.com/deepact/deepact/main/install.sh | sh
```
Installs to `/usr/local/bin`. Set `DEEPACT_INSTALL=~/.local/bin` to override.

**Windows (PowerShell):**
```powershell
powershell -c "irm https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.ps1 | iex"
```
Installs to `%LOCALAPPDATA%\deepact` and adds it to your user PATH.

**Go (if you have Go 1.24+):**
```bash
go install github.com/deepact/deepact@latest
```

**Manual:** Download the latest binary from [GitHub Releases](https://github.com/deepact/deepact/releases), extract, and place `deepact` (Linux/macOS) or `deepact.exe` (Windows) in your `$PATH`.

### Set your API key

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

# Headless / CI mode
deepact exec "fix the race condition in the connection pool"
```

On first run, DeepAct validates your API key before entering the TUI — so you never start typing only to discover a bad key.

## Slash Commands

| Command | Description |
|---|---|
| `/plan <goal>` | Explore the codebase and propose approaches |
| `/implement <goal>` | Jump directly to implementation |
| `/review` | Compare implementation against the plan |

## Keyboard Shortcuts

| Key | Action |
|---|---|
| `Enter` | Submit |
| `Ctrl+C` | Cancel current run / Quit |
| `Alt+Enter` | Insert newline |
| `Alt+drag` | Select and copy text |
| `↑↓` | Scroll through output |
| `Tab` | Autocomplete slash command |

## Architecture

```
cmd/          CLI entry point (Cobra)
ui/           Terminal UI (Bubble Tea)
engine/       Agent loop, guard system, conference pipeline, sub-agents
context/      Prompt builder, repo map, language packs, compression
llm/          DeepSeek API client (streaming, retry, rate limiting, echo management)
policy/       Ambiguity detection, design guard, scope guard
tools/        Built-in tools (read, write, edit, grep, glob, bash, fetch, revert)
session/      JSONL session persistence with fork/rewind
artifact/     Content-addressable storage with automatic secret redaction
router/       Dual-model routing (flash ↔ pro)
```

## Configuration

Create `~/.deepact/config.toml`:

```toml
[model]
default = "deepseek-v4-flash"    # Fast model for simple turns
escalation = "deepseek-v4-pro"   # Full model for planning & review

[context]
max_budget_tokens = 1048576

[guards]
scope_guard = true               # Block destructive ops without confirmation

[conference]
enabled = true
```

## License

MIT — see [LICENSE](LICENSE).
