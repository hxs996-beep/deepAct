# DeepAct — Lightweight Terminal AI Coding Agent

**English** | [简体中文](README.zh.md)

<p align="center">
  <a href="https://goreportcard.com/report/github.com/hxs996-beep/deepAct"><img src="https://img.shields.io/badge/go_report-A-brightgreen?style=flat-square" alt="Go Report"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=flat-square" alt="MIT"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go" alt="Go 1.24+"></a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey?style=flat-square" alt="Platforms">
</p>

<p align="center">
  <b>⚡ Single binary · Zero runtime deps</b>
</p>

**DeepAct is an AI coding agent that lives in your terminal** — written in Go, statically compiled, open source (MIT).

---

## 📖 Contents

1. [Quick Start](#quick-start)
2. [Day-to-Day Use](#day-to-day-use)
3. [Connecting a Model](#connecting-a-model)
4. [Design Philosophy](#design-philosophy)
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

# or build from source
git clone https://github.com/hxs996-beep/deepAct && cd deepAct && make install
```

Windows users: see [Releases](https://github.com/hxs996-beep/deepAct/releases) (PowerShell or manual download).

### Start Using

```bash
deepact                      # interactive TUI (Windows / macOS / Linux)
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
| `↑` | Recall the last submitted input (empty box) |

### Parallel Research (/collab)

Type `/collab add a cache layer` in the TUI. `/collab` is a built-in collaboration skill driven by `handoff_to_agent`: the main agent decomposes the goal into 2-6 research directions, then delegates each direction to a `researcher` sub-agent **in parallel** (read-only tools) and merges their findings into one structured report — faster than serial investigation when you need breadth quickly.

### Independent Review (/review)

Type `/review the current changes` in the TUI — or narrow the range, e.g. `/review against main`. `/review` is a built-in skill **you** invoke — it is excluded from the model-facing skill catalog, so the agent never starts a review on its own. The text after the command is the range, and it is required: a bare `/review` leaves the engine with no task, so it only loads the skill and starts nothing. The range is mapped to facts (`git status` + `git diff HEAD`, one commit, a branch, or a document), the diff plus a review rubric goes to an **independent** `sub` sub-agent — its own context, not the author's conversation — and each finding is verified against the code before it is reported. Findings carry a priority (P0–P3), a confidence score and a `file:line` citation; the reviewer is read-only, and nothing is fixed until you ask.

### Project Rules & Skills

Project conventions, workflows, and domain knowledge are injected into the system prompt via **skills**: the skill list is rendered into the stable zone, and the agent loads a skill's full instructions via the `load_skill` tool when your message names or clearly matches that skill. You can also load a skill directly with `/<name>`.

Built-in skills (`skill/builtin/`, go:embed: `ratd` / `collab` / `review`) load first at the lowest priority; user skills are loaded from common agent skill directories (`~/.claude/skills/`, `~/.agent/skills/`, `<project>/.claude/skills/`, `~/.deepact/skills/`), with `~/.deepact/skills/` winning on name conflicts. Skills are also installable from the community registry with `deepact`'s `skill_install` tool.

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

Register MCP servers in the project's `.deepact/mcp.json` — the one under the directory you run DeepAct in — falling back to `~/.deepact/mcp.json` only when that file is absent. Their tools join the available tool set automatically, no code changes needed; the two files are never merged, so just the first one found is used.

The `env` block carries non-secret values straight to the server — no `${VAR}` expansion applies — and the server already inherits your shell environment, so export tokens instead of writing them into the file:

```json
{
  "servers": [
    { "name": "github", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"] }
  ]
}
```

## Connecting a Model

`[model].base_url` takes any OpenAI-compatible endpoint — there is no provider mapping, and DeepSeek is simply the default. Point it at your provider and set the matching `api_key`:

Config example (`~/.deepact/config.toml`). API keys belong in this user-level file: a project-level `.deepact/config.toml` is applied *instead of* it, never merged with it, so if you keep project settings in the repo, pass the key through `DEEPSEEK_API_KEY` instead:

```toml
[model]
api_key = "sk-..."        # or: DEEPSEEK_API_KEY env var / first-launch TUI prompt
default = "deepseek-v4-flash"  # model for sub-agents and compaction
escalation = "<main-loop model>"  # model for the main loop (optional; unset = the flash model)
reasoning_effort = "high" # thinking effort: none | low | high | max (optional; default high)

[search]
provider    = "tavily"    # built-in web_search tool
api_key     = "tvly-..."
max_results = 5

[approval]
mode = "on-request"       # or "never": stop asking for project-level danger (system-level stays blocked)

[[approval.rules]]        # declared command rules, evaluated before the built-in judge
program  = "terraform"
args     = ["destroy"]    # token prefix; "*" matches any one token (empty = every invocation)
decision = "forbidden"    # allow | prompt | forbidden
reason   = "production only"
```

> [!NOTE]
> Dangerous commands pass four judgement layers, most authoritative first: (1) irreversible system-level operations — hard-blocked, no rule can unlock them; (2) `decision = "forbidden"` rules; (3) `decision = "allow"` rules and commands you confirmed in a session; (4) project-level danger and `decision = "prompt"` rules, which ask unless `mode = "never"`. A command you confirm in the popup is remembered **verbatim** and survives restarts (stored in `~/.deepact/approvals.toml`); to allow a whole family of commands, declare `[[approval.rules]]` explicitly instead of expecting one confirmation to widen itself.

> [!TIP]
> Every field is TOML-configurable — model, context budget, UI, LSP, MCP servers and user-defined sub-agent roles; the authoritative list is the `toml` tags on the `File` struct in `config/config.go`.

## Design Philosophy

Two rules shape every mechanism in DeepAct:

- **Generality first.** A mechanism answers a *class* of problems, not one scenario. Enumerating cases — a rule per situation, a heuristic per shape — is the signal that the design drifted off course, and the fix is to go back to the regularity underneath.
- **Capability over hardcoded judgment.** Wherever the model can decide — when to ask you, when to stop, how to weigh a tradeoff — the engine hands it a capability instead: a tool, an explicit channel, information it can read, and then stays out of the way. Fixed logic is reserved for what has to be deterministic: safety, data destruction, irreversible operations.

## CLI Reference

| Command | Description |
|---------|-------------|
| `deepact` | Interactive TUI (first launch prompts for the API key) |
| `deepact set api-key <key>` | Store the API key in `~/.deepact/config.toml` |

## Architecture

```text
cmd/      CLI entry (Cobra)         ui/       Terminal UI (Bubble Tea)
engine/   agent loop · subagents · shared type hub
context/  prompt build · tree snapshot · compaction   llm/      DeepSeek client (stream·retry·rate)
tools/    built-in tools (builtin/) + MCP (mcp/)
session/  JSONL session log & resume  artifact/ content-addressed store·auto-redact
skill/    built-in (ratd/collab/review) + external skill loading    config/    shared config
memory/   cross-session persistent memory
```

Layering: `engine/` is the shared type/interface hub (hub-and-spoke). Core `llm/` and `tools/` files have zero project imports; small `adapter.go` files bridge them to engine types. `engine/` never imports `ui/`/`cmd/`; cross-layer calls go through interfaces.

---

## License

[MIT](LICENSE)
