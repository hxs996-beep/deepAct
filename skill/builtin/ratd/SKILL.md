---
name: ratd
description: Reverse test-driven development (Red-Team-Then-Drive) — a red team writes adversarial tests first, then they drive the implementation. Use when strict verification of an implementation is needed and overconfidence must be prevented.
when_to_use: the user asks to "write tests before implementing", wants adversarial verification, or explicitly uses /ratd
disable-model-invocation: true
argument-hint: "<requirement>"
---

# Reverse test-driven (/ratd)

## Roles and orchestration (the model follows this script; it is not a hard pipeline)

You are the orchestrator, delegating to each role with `handoff_to_agent`, one delegation per role, at most 3 rounds each. All three roles are engine built-in named roles — their personas and read-only tool allowlists are built in, so `goal` carries only the task input:

### 1. Proposer
`handoff_to_agent` (**agent="proposer"**), with the requirement as `goal` (including constraints and acceptance criteria). The role outputs a CodePayload JSON:
```json
{"language":"go","files":[{"path":"relative/path","content":"full code"}],"design_notes":"design notes"}
```
After receiving the JSON, you (the orchestrator) write `files` to their paths with edit/write.

### 2. RedTeam
`handoff_to_agent` (**agent="redteam"**), with the implementation file list + on-disk paths as `goal`, asking for targeted adversarial tests. The role outputs a TestPayload JSON:
```json
{"tests":[{"path":"relative/path","content":"test code"}],"no_issues":false}
```
With no issues it outputs `{"tests":[],"no_issues":true}`. After receiving the JSON, you write `tests` to disk.

### 3. Sandbox (engine-side execution)
- You (the orchestrator) run the test command with `bash`; language mapping: go→`go test -timeout 60s ./...`.
- On failure, collect the output.

### 4. Arbitrator
`handoff_to_agent` (**agent="arbitrator"**), with the failing test output + implementation/test file paths as `goal`, asking it to judge. The role outputs a decision JSON:
```json
{"decision":"ACCEPT","feedback":"rework direction"} or {"decision":"REJECT","feedback":"red-team test defect"}
```
ACCEPT = the implementation is faulty; return to the Proposer. REJECT = the tests are faulty; delete the test file and return to the RedTeam.

## Completion criterion
- Sandbox passes and the red team reports no issues (no_issues=true) → `task_complete`, delivering a summary with the changed file list + final test result.
- You control the loop by discipline (≤3 rounds); `LoopTracker` is the backstop against role loops.

## Caveats
- Write the `goal` in the user's language; keep the tool calls themselves as described.
- The role personas already include the English JSON contract; the orchestrator must not repeat the role prompt.
