---
name: collab
description: Parallel research — split a research goal into several non-overlapping directions, delegate them to sub-agents in parallel, then synthesize a report. Use for deep analysis that needs multi-direction / multi-module investigation and cross-checking.
when_to_use: the user asks to "investigate", "analyze" or "research" a topic spanning several modules/directions, or explicitly uses /collab
disable-model-invocation: true
argument-hint: "<research goal>"
---

# Parallel research (/collab)

## Orchestration (the model decides)

0. **Planning phase** (do this first, no delegation): use read-only tools to absorb the background — the user's full original request, the relevant code, the directory tree, AGENTS.md, and existing patterns. Build a **global framework**: the goal, the rationale for the split, a first sketch of directions and how they relate.
1. **Decompose** (no delegation): based on the global framework, split the research goal into 2-6 non-overlapping directions. Each direction must be self-contained — an independent sub-agent shares no context, so it must be able to start from the direction description + context alone. When recording directions with `todo_write`, give each a short stable `id` (e.g. `dir-1`); the UI uses it to nest the sub-agent under that direction.
2. **Delegate in parallel**: issue one `handoff_to_agent` per direction (**agent="researcher"**, the engine's built-in read-only investigation role, with its own researcher persona and read-only tool allowlist), **in a single turn** (the engine runs them in parallel). Parameters per handoff:
   - `goal`: **the direction description only** (the role persona is built into `researcher`; do not repeat it)
   - `step`: the `id` of this direction from `todo_write`; the UI indents the delegated sub-agent under that direction. Omit it when the delegation does not map to a direction
   - `context`: **required**, containing three parts:
     a. the **user's full original request** (so the sub-agent understands the problem)
     b. the **global framework** (goal, split rationale, how the directions relate, from the planning phase)
     c. **this direction's remit** (what it owns, its boundary, which directions it complements, to avoid duplicated work)
     The context must be detailed enough that the sub-agent can start without re-exploring the background.
   - `expected_output`: `"Research note: conclusion + key evidence with file:line references"`
   - (optional) `constraints`: only to add constraints beyond the role persona; a pure read-only investigation needs no "do not modify files" — the researcher allowlist is already read-only.
3. **Synthesize** (no delegation): merge the worker reports, cross-analyze against the global framework (find contradictions, verify evidence, mark unverified assumptions), and produce a structured research report.

## Notes
- `researcher` is an engine built-in named role (`AgentResearcher`): a stable persona (an independent investigator), a read-only tool allowlist (`read/grep/glob/lsp`), a **15-turn cap**, and a **structured role** (it must call `submit_result` to submit its note — plain text does not complete it). The `goal` only carries the direction.
- When all directions are done → `task_complete`, delivering: overall conclusion + per-direction findings + cross-direction analysis + unverified assumptions.
- A single direction failing: you decide whether to retry / degrade / mark it; never hide a failure.

## Caveats
- Write the `goal` in the user's language; keep the tool calls themselves as described.
- Depth discipline: every direction must actually read code for evidence — no impressions; cross-check the file:line citations in worker results.
