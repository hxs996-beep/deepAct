# Corrected Analysis: Orphan `tool_calls` Bug in Guard-Block Path

## Root Cause

In `engine/turn.go`, when `executeTurn()` generates an assistant message with `ToolCalls` (line 192), it appends it to `e.history`. Then the loop at lines 194-206 checks each tool call against guards. If **any** guard blocks, the function returns early with `TurnResult{Blocked: true}` — but the assistant message (with `ToolCalls`) is **already** in `e.history`.

On the next `executeTurn()` call, `context.Build()` converts `e.history` to `ModelMessage` slices via `mapMessage()` (context/builder.go:195-216), which faithfully passes `ToolCalls` through. This produces an API message sequence where an `assistant` message with `tool_calls` has no corresponding `tool` messages, violating DeepSeek's API requirement:

> "assistant message with 'tool_calls' must be followed by tool messages matching tool_call_id."

This causes a 400 Bad Request error.

## Bug Location

- **File:** `engine/turn.go`, lines 192-206
- **Line 192:** `e.history = append(e.history, assistant)` — assistant message with `ToolCalls` is committed to history
- **Lines 196-197:** Loop guard blocks (returns `TurnResult{Blocked: true}`)
- **Lines 200-205:** Scope guard blocks (returns `TurnResult{Blocked: true}`)
- Both return paths leave orphan `tool_calls` in history

## Fix Approach

**Principle:** Before returning from a guard block, add a `tool` message to `e.history` for each tool call that was pending, closing the matching `tool_call_id`. This ensures the message sequence is valid for the API.

### Detailed Fix

In `turn.go`, modify the guard block paths at lines 196-197 and 200-205:

#### Path 1: Loop Guard Block (lines 196-197)

**Before:**
```go
loopAction := e.guards.loop.Check(call)
if loopAction.Type != GuardAllow {
    return TurnResult{Blocked: true, BlockedBy: loopAction.Type, Questions: []string{loopAction.Message}}, nil
}
```

**After:**
```go
loopAction := e.guards.loop.Check(call)
if loopAction.Type != GuardAllow {
    // Add tool messages for ALL pending calls to close their tool_call_ids
    for _, c := range calls {
        toolMsg := Message{
            Role:       "tool",
            ToolCallID: c.ID,
            Content:    "Blocked: " + loopAction.Message,
            Timestamp:  time.Now(),
        }
        e.history = append(e.history, toolMsg)
    }
    return TurnResult{Blocked: true, BlockedBy: loopAction.Type, Questions: []string{loopAction.Message}}, nil
}
```

#### Path 2: Scope Guard Block (lines 200-205)

**Before:**
```go
scopeAction := e.guards.scope.CheckTool(call, e.state)
if scopeAction.Type != GuardAllow {
    // If blocked due to dangerous bash command, store it as pending user confirmation
    if call.Name == "bash" {
        e.state.PendingDangerousCmd = e.guards.scope.DangerousPending()
    }
    return TurnResult{Blocked: true, BlockedBy: scopeAction.Type, Questions: []string{scopeAction.Message}}, nil
}
```

**After:**
```go
scopeAction := e.guards.scope.CheckTool(call, e.state)
if scopeAction.Type != GuardAllow {
    // If blocked due to dangerous bash command, store it as pending user confirmation
    if call.Name == "bash" {
        e.state.PendingDangerousCmd = e.guards.scope.DangerousPending()
    }
    // Add tool messages for ALL pending calls to close their tool_call_ids
    for _, c := range calls {
        toolMsg := Message{
            Role:       "tool",
            ToolCallID: c.ID,
            Content:    "Blocked: " + scopeAction.Message,
            Timestamp:  time.Now(),
        }
        e.history = append(e.history, toolMsg)
    }
    return TurnResult{Blocked: true, BlockedBy: scopeAction.Type, Questions: []string{scopeAction.Message}}, nil
}
```

**Key differences between the two paths:**
- Loop guard path: uses `loopAction.Message` in the tool message content
- Scope guard path: uses `scopeAction.Message` in the tool message content, AND stores `PendingDangerousCmd` for the user confirmation flow

### Tool Message Content Distinction

The tool message content should differentiate between guard types to help the LLM understand context on re-execution:

| Guard Result Type | Tool Message Content | User-Facing Behavior |
|---|---|---|
| `GuardBlock` (loop) | `"Blocked: Loop detected: read \"foo.go\" repeated 3 times..."` | Hard block — LLM should change strategy |
| `GuardBlock` (scope, system-level) | `"Blocked: ❌ System-level dangerous command blocked (irreversible): ..."` | Hard block — command cannot be executed |
| `GuardAskUser` (scope, project-level) | `"Blocked: ⚠️ Dangerous command detected: ... Execute this command? Reply 'yes'..."` | Soft block — user can confirm, then LLM should re-issue |

For `GuardAskUser`, the "Blocked: ⚠️ ..." content is informative to the LLM. When the user confirms (loop.go:220-228), the LLM receives this tool message in history on the next turn, and should understand it's informational — the blocked call needs to be re-issued.

## Functional Requirements

### FR-1: Close orphan tool_call_ids when loop guard blocks
When the loop guard (`LoopGuard.Check()`) returns `GuardBlock`, the system MUST add a `tool` message to `e.history` for EACH pending tool call (`calls` slice) with `ToolCallID` matching the call ID, before returning the `TurnResult{Blocked: true}`.

### FR-2: Close orphan tool_call_ids when scope guard blocks
When the scope guard (`ScopeGuard.CheckTool()`) returns `GuardBlock` or `GuardAskUser`, the system MUST add a `tool` message to `e.history` for EACH pending tool call, before returning the `TurnResult{Blocked: true}`.

### FR-3: Preserve `PendingDangerousCmd` state for user confirmation
When the scope guard blocks a dangerous bash command with `GuardAskUser`, the system MUST continue to store the pending command via `e.state.PendingDangerousCmd = e.guards.scope.DangerousPending()`. This is already done at line 202-203 and must be preserved.

### FR-4: Preserve normal (non-blocked) execution path
When all guards return `GuardAllow`, the system MUST behave exactly as before — no tool messages are added prematurely.

### FR-5: Multi-tool-call correctness
When the assistant message contains MULTIPLE tool calls and ANY one is blocked, tool messages MUST be added for ALL pending calls, not just the blocked one. This ensures the full set of `tool_call_id` values are closed.

### FR-6: Unit tests covering guard block paths
Add unit tests in `engine/turn_test.go` that verify:
- Loop guard block adds correct tool messages
- Scope guard block (`GuardBlock`) adds correct tool messages
- Scope guard block (`GuardAskUser`) adds correct tool messages and preserves `PendingDangerousCmd`
- Normal (non-blocked) path is unaffected
- Multi-call scenario (3 calls, 1 blocked) produces tool messages for all 3

## Components Affected

| Component | File | Impact |
|-----------|------|--------|
| `executeTurn()` | `engine/turn.go:32-252` | **Primary fix target** — modify guard block return paths (lines 196-197, 200-205) |
| `Run()` | `engine/loop.go:260-261` | **No change needed** — already handles `TurnResult.Blocked` by returning to caller; history now includes valid tool messages |
| `Build()` | `context/builder.go:59-126` | **No change needed** — receives valid history with closed tool_call_ids |
| `mapMessage()` | `context/builder.go:195-216` | **No change needed** — passes ToolCalls through faithfully (which is correct when tool messages follow) |
| `findSafeSplitPoint()` | `engine/compressor.go:357-379` | **No change needed** — already avoids splitting between assistant-with-tool_calls and tool messages |
| `GuardSystem` | `engine/guards.go:21-24` | **No change needed** — guard logic is correct, only the block-return path needs fixing |
| `ScopeGuard.CheckTool()` | `engine/guards.go:136-156` | **No change needed** — returns `GuardBlock`/`GuardAskUser` correctly |

## Acceptance Criteria

### AC-1: Loop guard block — tool messages added
Given an assistant message with 2 tool calls, when the loop guard blocks the first call (e.g., loop detected on `read`), THEN `e.history` contains:
- The assistant message with 2 `ToolCalls`
- 2 `tool` messages (one per call) with matching `ToolCallID` and `Content` containing `"Blocked: Loop detected"`
- The next `executeTurn()` sends a valid message sequence accepted by DeepSeek API

### AC-2: Scope guard block — tool messages added
Given an assistant message with 2 tool calls (e.g., `bash rm -rf /` and `read foo.go`), when the scope guard blocks the bash call with `GuardBlock`, THEN:
- `e.history` contains 2 `tool` messages closing both `tool_call_id` values
- The `bash` tool message content includes `"❌ System-level dangerous command blocked"`
- The `read` tool message content includes `"Blocked: ❌ System-level..."` (same message for all calls)

### AC-3: Normal path unaffected
Given an assistant message with tool calls that all pass both guards, THEN no premature tool messages are added, and execution proceeds as before — handoff calls and regular tool calls are executed, and their results produce tool messages normally.

### AC-4: History is in correct state for subsequent turns
After a guard block, `e.history` must satisfy:

1. Every `assistant` message with non-empty `ToolCalls` is followed by at least `len(ToolCalls)` `tool` messages whose `ToolCallID` values form a superset of the assistant's tool call IDs.
2. `context.Build()` produces a `ModelMessage` sequence where every `assistant` message with `tool_calls` is followed by corresponding `tool` messages — this is what DeepSeek API requires.
3. The compressor's `findSafeSplitPoint()` can safely split at any assistant-without-tool_calls or user/system boundary without leaving orphan tool messages.

### AC-5: Multi-tool-call scenario — partial block
Given an assistant message with 3 tool calls (A, B, C) where:
- A passes loop guard but fails scope guard (`GuardAskUser`)
- B and C haven't been checked yet (loop stops at A)

THEN tool messages are added for A, B, and C (all pending calls in the `calls` slice), closing all 3 `tool_call_id` values.

## Hidden Assumptions and Risks

### 1. Multi-call blocking semantics
**Assumption:** When one tool call is blocked, ALL pending calls in that turn should be blocked (no partial execution). This is already the current behavior — guards are checked in a loop and the first block aborts. The fix preserves this by adding tool messages for ALL pending calls, not just the blocked one.

### 2. Tool message content for non-blocked calls
**Assumption:** When call A is blocked but call B was never checked, the tool message for B uses the SAME blocking message as A. This is acceptable because:
- The LLM sees all calls were blocked and adjusts its strategy
- The alternative (distinguishing "not executed because earlier call blocked" vs "blocked by guard") adds complexity for no clear benefit

### 3. GuardAskUser re-execution flow
**Risk:** When scope guard returns `GuardAskUser` for a dangerous bash command:

1. `executeTurn()` adds tool messages with `"Blocked: ⚠️ Dangerous command detected: ..."` and stores `PendingDangerousCmd`
2. `Run()` (loop.go:260) returns `EngineResponse{Blocked: true}` to the caller
3. User sees the prompt, replies "yes"
4. `Run()` (loop.go:220-228): calls `ConfirmDangerous()`, clears `PendingDangerousCmd`, adds user confirmation message
5. Next `executeTurn()`: history now contains assistant-with-tool_calls + tool("Blocked: ...") messages
6. The LLM must understand these "Blocked" tool messages are informational — the command is now authorized but the LLM needs to **RE-ISSUE** the tool call

**This re-execution flow works correctly** because:
- The `PendingDangerousCmd` is cleared so the scope guard won't block the re-issued call
- `ConfirmDangerous()` marks the command as confirmed in `dangerousConfirmed` map (guards.go:124-128)
- The LLM sees the tool message and knows it should re-issue

**But there's a UX caveat:** The LLM might not reliably re-issue the blocked call. The tool message content should clearly indicate it's a prompt for re-execution, e.g., `"Blocked: ⚠️ Dangerous command detected. User must confirm. After confirmation, please re-issue this command."`

### 4. History length increase
**Risk:** Adding tool messages on guard blocks increases history length slightly (1 message per pending call). This is negligible — typical guard blocks have 1-3 pending calls.

### 5. Compressor interaction
**Assumption:** The compressor's `findSafeSplitPoint()` (compressor.go:357-379) already avoids splitting between an assistant-with-tool_calls and its tool messages. After the fix, this invariant is preserved because tool messages always immediately follow the assistant message.

### 6. Sub-agents (handoff) unaffected
**Observation:** Sub-agents called via `handoff_to_agent` do NOT go through guard checks (they're separated at line 213-218 and executed separately). The fix doesn't affect this path.

### 7. DeepSeek-specific constraint
**Assumption:** The requirement "assistant message with 'tool_calls' must be followed by tool messages matching tool_call_id" is specific to DeepSeek. Other providers (OpenAI, Anthropic) may be more lenient. The fix should treat all providers uniformly — valid message sequences are always better.

### 8. Loop guard state management
**Verified:** The loop guard (`LoopGuard.Check()`) does NOT increment its counter when blocking — `count >= g.maxRepeats` returns `GuardBlock` at line 47-51 before `g.entries = append(...)` at line 53. So a blocked loop call doesn't count toward future loop detection. This is correct behavior.

## Test Specification

### Test File
`engine/turn_test.go` — add to existing test file, or create if not exists.

### Test Structure
Use table-driven tests with mock guards.

```go
type mockGuard struct {
    action GuardAction
}

func (m *mockGuard) Check(call ToolCallRequest) GuardAction {
    return m.action
}
```

### Test Cases

| Test Case | Guard Behavior | Expected |
|-----------|---------------|----------|
| Loop guard blocks first call | loop.Check → GuardBlock | 2 tool messages added to history |
| Scope guard blocks with GuardBlock | scope.CheckTool → GuardBlock | 2 tool messages, PendingDangerousCmd set |
| Scope guard blocks with GuardAskUser | scope.CheckTool → GuardAskUser | 2 tool messages, PendingDangerousCmd set |
| All guards allow (normal path) | Both → GuardAllow | No extra tool messages, execution continues |
| Multi-call (3 calls, 1st blocked) | GuardBlock on call 0 | 3 tool messages added (all calls) |
| Empty calls (no tool calls) | N/A | No guard check, normal return |

### Verification
After each test case, verify:
- `len(e.history)` has expected number of entries
- Last N entries are `tool` messages with correct `ToolCallID` values
- `ToolCallID` values match the original `MessageToolCall.ID` values
- Content contains "Blocked:" for blocked cases
- `PendingDangerousCmd` is set correctly for scope guard blocks on bash
