# Analysis Report Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an engine-level gate that forces the agent to present a text-only analysis report before transitioning from search to implementation, and enhance the edit plan summary with structured file change details.

**Architecture:** Three components: (1) persist the `[ANALYSIS MODE]` constraint in TaskState so it survives across turns, injected by `context/builder.go` on every `Build` call; (2) a new gate in `turn.go` that intercepts the first edit/write attempt after searches, injecting a nudge that forces the agent to output an analysis report, with confirmation handling in `loop.go`; (3) enhance `formatEditPlanSummary` to show file paths and edit previews alongside the reasoning text.

**Tech Stack:** Go 1.21+, existing engine/loop/turn/context architecture

## Global Constraints

- Go module: `github.com/deepact/deepact`
- Existing patterns: table-driven tests, `strings.Builder` for formatting, inline intent switch in `Run()`
- No new dependencies
- JSON tags on all new TaskState fields for serialization
- Bilingual messages (zh/en) matching existing `e.isChinese` / `a.userLang == "中文"` pattern
- Tests are internal (same package as code under test)

## File Structure

| File | Responsibility |
|------|---------------|
| `engine/types.go` | TaskState struct — add `AnalysisMode`, `AnalysisReportConfirmed` fields |
| `engine/loop.go` | Engine struct — add `pendingAnalysisNudge`, `analysisNudgeCount`; intent switch — set/clear AnalysisMode; `handleAnalysisNudgeConfirmation` method; resets |
| `engine/turn.go` | Analysis report gate before edit plan guard; enhanced `formatEditPlanSummary` |
| `context/builder.go` | Inject `[ANALYSIS MODE]` constraint text when `state.AnalysisMode == true` |
| `engine/analysis_gate_test.go` | New test file for confirmation handling |
| `context/builder_test.go` | Add test for ANALYSIS MODE constraint injection |
| `engine/turn_editplan_test.go` | Add tests for enhanced summary |

---

### Task 1: New Fields + Persistent ANALYSIS MODE Constraint

**Files:**
- Modify: `engine/types.go:241` (add fields after `ReadHistory`)
- Modify: `engine/loop.go:92` (add Engine struct fields after `pendingEditPlan`)
- Modify: `engine/loop.go:249` (reset `analysisNudgeCount` at start of `Run()`)
- Modify: `engine/loop.go:591-607` (intent switch: set/clear `AnalysisMode`)
- Modify: `engine/loop.go:1291` (reset in clear function)
- Modify: `context/builder.go:150` (inject constraint after Block B)
- Test: `context/builder_test.go` (add test)

**Interfaces:**
- Consumes: `engine.TaskState` (existing struct), `ContextAssembler.Build` (existing method)
- Produces: `TaskState.AnalysisMode bool` (JSON `"analysis_mode,omitempty"`), `TaskState.AnalysisReportConfirmed bool` (JSON `"analysis_report_confirmed,omitempty"`), `Engine.pendingAnalysisNudge bool`, `Engine.analysisNudgeCount int`

- [ ] **Step 1: Write the failing test**

Add to `context/builder_test.go` (append to existing file, inside `package context`):

```go
func TestBuild_AnalysisModeConstraint(t *testing.T) {
	assembler := NewContextAssembler(".", nil)
	assembler.userLang = "中文"
	assembler.userLangSet = true
	assembler.stableSessionBlock = "stable"

	// AnalysisMode=true: constraint should be present
	state := &engine.TaskState{
		Goal:         "test goal",
		AnalysisMode: true,
	}
	msgs := assembler.Build(state, nil, nil)
	found := false
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "[ANALYSIS MODE]") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Build with AnalysisMode=true should include [ANALYSIS MODE] constraint")
	}

	// AnalysisMode=false: constraint should NOT be present
	state.AnalysisMode = false
	msgs = assembler.Build(state, nil, nil)
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "[ANALYSIS MODE]") {
			t.Errorf("Build with AnalysisMode=false should NOT include [ANALYSIS MODE] constraint")
			break
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/admin/gitspace/deepact && go test ./context/ -run TestBuild_AnalysisModeConstraint -v`
Expected: FAIL — `state.AnalysisMode` undefined (field does not exist on `engine.TaskState`)

- [ ] **Step 3: Add TaskState fields**

In `engine/types.go`, after line 241 (`ReadHistory []ReadRecord ...`), add two new fields:

```go
	ReadHistory []ReadRecord `json:"read_history"`

	// AnalysisMode is set when the user's intent is analysis-only. When true,
	// the context builder injects a [ANALYSIS MODE] constraint every turn,
	// persisting across turns (unlike the former pendingPinnedMessages approach
	// which was cleared after the first turn). Cleared when the user confirms
	// the analysis report or starts a new topic.
	AnalysisMode bool `json:"analysis_mode,omitempty"`

	// AnalysisReportConfirmed is set when the user confirms the analysis report
	// presented by the agent. When true, the analysis report gate is skipped,
	// allowing the edit plan guard to proceed normally.
	AnalysisReportConfirmed bool `json:"analysis_report_confirmed,omitempty"`
```

- [ ] **Step 4: Add Engine struct fields**

In `engine/loop.go`, after line 92 (`pendingEditPlan *PendingEditPlan`), add:

```go
	pendingEditPlan *PendingEditPlan

	// pendingAnalysisNudge is true when the analysis report gate has blocked
	// edit/write calls, waiting for the agent to output a text-only analysis
	// report. Persists across Run() calls (set in one Run, checked in the next
	// when the user confirms). Cleared on user confirmation, feedback, or
	// session reset.
	pendingAnalysisNudge bool

	// analysisNudgeCount tracks how many times the analysis report gate has
	// blocked within the current Run(). After 2 blocks, the gate stops
	// intercepting and lets the edit plan guard take over (degraded mode).
	// Reset to 0 at the start of each Run().
	analysisNudgeCount int
```

- [ ] **Step 5: Add constraint injection in builder.go**

In `context/builder.go`, after line 150 (the `blockB` append) and before `return messages` (line 152), insert:

```go
	// Analysis mode constraint: when the user's intent is analysis-only, inject
	// the constraint on every Build call so it persists across turns. The former
	// approach used pendingPinnedMessages which was cleared after the first turn.
	if state != nil && state.AnalysisMode {
		constraint := "[ANALYSIS MODE] 用户要求仅进行分析，不要修改任何代码。你的任务仅限于：阅读代码、分析原因、解释行为。禁止：edit、write、或任何修改文件的操作。"
		if a.userLang != "中文" {
			constraint = "[ANALYSIS MODE] The user asked for analysis only. Do NOT modify any code. Your task is limited to: reading code, analyzing causes, explaining behavior. FORBIDDEN: edit, write, or any file modification operations."
		}
		messages = append(messages, engine.ModelMessage{Role: "user", Content: constraint})
	}
```

- [ ] **Step 6: Modify intent switch in loop.go**

In `engine/loop.go`, replace lines 591-607 (the three intent branches). Find this exact code:

```go
	case IntentAnalyze:
		if skillJustActivated {
			loopLog.Printf("intent: analyze skipped - skill activation takes priority")
		} else {
			e.state.PlanConfirmed = false
			constraint := "[ANALYSIS MODE] 用户要求仅进行分析，不要修改任何代码。你的任务仅限于：阅读代码、分析原因、解释行为。禁止：edit、write、或任何修改文件的操作。"
			if !zh {
				constraint = "[ANALYSIS MODE] The user asked for analysis only. Do NOT modify any code. Your task is limited to: reading code, analyzing causes, explaining behavior. FORBIDDEN: edit, write, or any file modification operations."
			}
			e.pendingPinnedMessages = append(e.pendingPinnedMessages, constraint)
			loopLog.Printf("intent: analyze-only, reset PlanConfirmed + injected constraint")
		}
	case IntentNewTopic:
		e.state.PlanConfirmed = false
		loopLog.Printf("intent: new topic, reset PlanConfirmed (was %q)", e.state.Goal)
	default: // IntentContinue
		loopLog.Printf("intent: continue, keeping PlanConfirmed=%v", e.state.PlanConfirmed)
```

Replace with:

```go
	case IntentAnalyze:
		if skillJustActivated {
			loopLog.Printf("intent: analyze skipped - skill activation takes priority")
		} else {
			e.state.PlanConfirmed = false
			e.state.AnalysisMode = true
			e.state.AnalysisReportConfirmed = false
			loopLog.Printf("intent: analyze-only, set AnalysisMode=true (persistent)")
		}
	case IntentNewTopic:
		e.state.PlanConfirmed = false
		e.state.AnalysisMode = false
		e.state.AnalysisReportConfirmed = false
		e.pendingAnalysisNudge = false
		loopLog.Printf("intent: new topic, reset PlanConfirmed + AnalysisMode")
	default: // IntentContinue
		// Clear analysis mode - the user is continuing with implementation.
		// Keep AnalysisReportConfirmed as-is: if the user confirmed the report,
		// it stays confirmed; if not, the gate will still fire.
		e.state.AnalysisMode = false
		loopLog.Printf("intent: continue, cleared AnalysisMode, keeping PlanConfirmed=%v", e.state.PlanConfirmed)
```

- [ ] **Step 7: Add resets**

In `engine/loop.go`, after line 249 (`e.runToolCallCount = 0`), add:

```go
	e.runToolCallCount = 0
	e.analysisNudgeCount = 0
```

In `engine/loop.go`, after line 1291 (`e.pendingEditPlan = nil`), add:

```go
	e.pendingEditPlan = nil
	e.pendingAnalysisNudge = false
	e.analysisNudgeCount = 0
	e.state.AnalysisMode = false
	e.state.AnalysisReportConfirmed = false
```

- [ ] **Step 8: Run test to verify it passes**

Run: `cd /Users/admin/gitspace/deepact && go test ./context/ -run TestBuild_AnalysisModeConstraint -v`
Expected: PASS

- [ ] **Step 9: Run full test suite to verify no regressions**

Run: `cd /Users/admin/gitspace/deepact && go test ./context/ ./engine/ -count=1 -timeout 60s 2>&1 | tail -20`
Expected: All existing tests still pass (new fields default to zero values)

- [ ] **Step 10: Commit**

```bash
cd /Users/admin/gitspace/deepact && git add engine/types.go engine/loop.go context/builder.go context/builder_test.go && git commit -m "feat: persist ANALYSIS MODE constraint across turns

Add AnalysisMode and AnalysisReportConfirmed to TaskState. The constraint
is now injected by context/builder.go on every Build call instead of the
ephemeral pendingPinnedMessages approach that was cleared after one turn.

Also adds pendingAnalysisNudge and analysisNudgeCount to the Engine struct
for use by the analysis report gate (Task 2)."
```

---

### Task 2: Analysis Report Gate + Confirmation Handling

**Files:**
- Modify: `engine/loop.go:407` (add `handleAnalysisNudgeConfirmation` call in `Run()`)
- Create: `engine/analysis_gate_test.go` (new test file)
- Modify: `engine/turn.go:307` (add gate before edit plan guard)

**Interfaces:**
- Consumes: `Engine.pendingAnalysisNudge` (from Task 1), `Engine.runToolCallCount` (existing), `Engine.analysisNudgeCount` (from Task 1), `TaskState.AnalysisReportConfirmed` (from Task 1), `isDangerousConfirmation` (existing function in loop.go)
- Produces: `Engine.handleAnalysisNudgeConfirmation(userMsg string) bool` (new method — returns true if a nudge was pending and handled), gate block in `executeTurn` that sets `pendingAnalysisNudge = true` and injects a nudge message

- [ ] **Step 1: Write the failing test**

Create `engine/analysis_gate_test.go`:

```go
package engine

import (
	"testing"
)

func TestHandleAnalysisNudgeConfirmation(t *testing.T) {
	// Case 1: user confirms the analysis report
	e := &Engine{
		state:                 &TaskState{AnalysisMode: true, AnalysisReportConfirmed: false},
		pendingAnalysisNudge:  true,
		isChinese:             true,
		history:               []Message{{Role: "user", Content: "确认"}},
	}
	handled := e.handleAnalysisNudgeConfirmation("确认")
	if !handled {
		t.Error("should return true when nudge is pending")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after confirmation")
	}
	if e.state.AnalysisMode {
		t.Error("AnalysisMode should be false after confirmation")
	}
	if e.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after confirmation")
	}

	// Case 2: user gives feedback (not a confirmation)
	e2 := &Engine{
		state:                &TaskState{AnalysisMode: true, AnalysisReportConfirmed: false},
		pendingAnalysisNudge: true,
		isChinese:            true,
		history:              []Message{{Role: "user", Content: "不对"}},
	}
	handled2 := e2.handleAnalysisNudgeConfirmation("不对")
	if !handled2 {
		t.Error("should return true when nudge is pending (feedback)")
	}
	if e2.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should remain false after feedback")
	}
	if e2.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after feedback")
	}

	// Case 3: no nudge pending — should return false
	e3 := &Engine{state: &TaskState{}}
	handled3 := e3.handleAnalysisNudgeConfirmation("确认")
	if handled3 {
		t.Error("should return false when no nudge is pending")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/admin/gitspace/deepact && go test ./engine/ -run TestHandleAnalysisNudgeConfirmation -v`
Expected: FAIL — `e.handleAnalysisNudgeConfirmation undefined`

- [ ] **Step 3: Implement `handleAnalysisNudgeConfirmation` method**

In `engine/loop.go`, add this new method after the `detectUserIntent` function (after line ~1270, before the `Reset` function). Find the end of `detectUserIntent` and add:

```go
// handleAnalysisNudgeConfirmation processes the user's response to an analysis
// report nudge. When the analysis report gate blocks edit/write calls, the
// agent outputs a text-only report (ending the Run()), and the user responds.
// If the user confirms, AnalysisReportConfirmed is set so the gate skips on
// the next edit attempt. If the user gives feedback, the nudge is cleared so
// the agent can re-analyze. Returns true if a nudge was pending and handled.
func (e *Engine) handleAnalysisNudgeConfirmation(userMsg string) bool {
	if !e.pendingAnalysisNudge {
		return false
	}
	if isDangerousConfirmation(userMsg) {
		e.state.AnalysisReportConfirmed = true
		e.state.AnalysisMode = false
		e.pendingAnalysisNudge = false
		// Replace the user's bare confirmation with a contextual message so
		// the agent knows the analysis was approved and can proceed to edit.
		if len(e.history) > 0 && e.history[len(e.history)-1].Role == "user" {
			msg := "✅ 分析报告已确认，可以开始修改代码。"
			if !e.isChinese {
				msg = "✅ Analysis report confirmed. You may now proceed with code changes."
			}
			e.history[len(e.history)-1].Content = msg
		}
		loopLog.Printf("analysis nudge confirmed by user")
	} else {
		// User is providing feedback on the analysis, not confirming.
		// Contextualize so the agent understands this is feedback and should
		// re-analyze rather than proceed to edit.
		if len(e.history) > 0 && e.history[len(e.history)-1].Role == "user" {
			if e.isChinese {
				e.history[len(e.history)-1].Content = fmt.Sprintf(
					"用户对分析报告给出了反馈：%s\n\n请根据反馈重新分析，然后再次输出完整的分析报告。",
					userMsg,
				)
			} else {
				e.history[len(e.history)-1].Content = fmt.Sprintf(
					"The user provided feedback on the analysis report: %s\n\nRe-analyze based on the feedback, then output a complete analysis report again.",
					userMsg,
				)
			}
		}
		e.pendingAnalysisNudge = false
		e.state.AnalysisReportConfirmed = false
		loopLog.Printf("analysis nudge feedback from user (not confirmation)")
	}
	return true
}
```

- [ ] **Step 4: Call the method in Run()**

In `engine/loop.go`, after line 407 (`e.updateGoalFromFirstMessage(userMsg)`) and before line 409 (`if e.pendingEditPlan != nil {`), insert:

```go
	e.updateGoalFromFirstMessage(userMsg)

	// Analysis report nudge: if the gate blocked in the previous Run() and the
	// agent produced a text-only analysis report, handle the user's response
	// (confirmation or feedback) before any other processing.
	e.handleAnalysisNudgeConfirmation(userMsg)

	if e.pendingEditPlan != nil {
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /Users/admin/gitspace/deepact && go test ./engine/ -run TestHandleAnalysisNudgeConfirmation -v`
Expected: PASS

- [ ] **Step 6: Add the analysis report gate in turn.go**

In `engine/turn.go`, before line 307 (the edit plan guard comment block starting with `// Edit plan guard:`), insert the gate. Find this exact code at line 306-310:

```go
	// PlanConfirmed escape hatch - death-loop-safe.

	// Edit plan guard: before executing any edit/write calls for the first time
	// in this Run(), block and present the agent's understanding + proposed changes
	// to the user for approval.
	if e.pendingEditPlan == nil && !e.state.PlanConfirmed {
```

Insert BEFORE the `// Edit plan guard` comment:

```go
	// PlanConfirmed escape hatch - death-loop-safe.

	// Analysis report gate: before allowing edit/write, require the agent to
	// present a text-only analysis report and get user confirmation. This gate
	// fires when the agent has done searches (runToolCallCount > 0) and is now
	// attempting to modify code, but hasn't yet presented its findings to the
	// user. After 2 blocks, the gate gives up and lets the edit plan guard
	// take over (degraded mode - better than deadlocking).
	if e.runToolCallCount > 0 && !e.state.AnalysisReportConfirmed &&
		!e.state.PlanConfirmed && e.pendingEditPlan == nil &&
		e.analysisNudgeCount < 2 {
		var editCalls []ToolCallRequest
		for _, call := range calls {
			if call.Name == "edit" || call.Name == "write" {
				editCalls = append(editCalls, call)
			}
		}
		if len(editCalls) > 0 {
			e.analysisNudgeCount++
			turnLog.Printf("analysis report gate: blocking %d edit/write call(s) (nudgeCount=%d, runToolCallCount=%d)",
				len(editCalls), e.analysisNudgeCount, e.runToolCallCount)
			nudgeMsg := "在修改代码之前，请先输出完整的分析报告：\n" +
				"1. 你发现了什么（列出具体位置和代码）\n" +
				"2. 为什么需要修改\n" +
				"3. 计划改哪些文件、怎么改\n" +
				"输出报告后停止，等待用户确认再执行修改。"
			if !e.isChinese {
				nudgeMsg = "Before making changes, output a complete analysis report:\n" +
					"1. What you found (list specific locations and code)\n" +
					"2. Why changes are needed\n" +
					"3. Which files you plan to change and how\n" +
					"Stop after the report and wait for user confirmation before modifying."
			}
			e.pendingAnalysisNudge = true
			e.history = append(e.history, assistant)
			for _, c := range calls {
				e.history = append(e.history, Message{
					Role:       "tool",
					ToolCallID: c.ID,
					Content:    "Blocked: " + nudgeMsg,
					Timestamp:  time.Now(),
				})
			}
			return TurnResult{Done: false, FinishReason: finish}, nil
		}
	}

	// Edit plan guard: before executing any edit/write calls for the first time
	// in this Run(), block and present the agent's understanding + proposed changes
	// to the user for approval.
	if e.pendingEditPlan == nil && !e.state.PlanConfirmed {
```

- [ ] **Step 7: Run full engine test suite**

Run: `cd /Users/admin/gitspace/deepact && go test ./engine/ -count=1 -timeout 60s 2>&1 | tail -20`
Expected: All tests pass (the gate is inert when `runToolCallCount == 0` or `AnalysisReportConfirmed == true`, which is the default state)

- [ ] **Step 8: Commit**

```bash
cd /Users/admin/gitspace/deepact && git add engine/loop.go engine/turn.go engine/analysis_gate_test.go && git commit -m "feat: add analysis report gate before edit plan guard

When the agent has done searches (runToolCallCount > 0) and attempts
edit/write for the first time, the gate blocks and injects a nudge
asking the agent to output a text-only analysis report. The agent
outputs the report, stop hooks end the Run(), and the user confirms
before implementation proceeds.

After 2 blocks the gate degrades to the edit plan guard to avoid
deadlock. Confirmation handling is extracted into
handleAnalysisNudgeConfirmation for testability."
```

---

### Task 3: Enhanced Edit Plan Summary

**Files:**
- Modify: `engine/turn.go:1340-1355` (replace `formatEditPlanSummary`)
- Test: `engine/turn_editplan_test.go` (add tests)

**Interfaces:**
- Consumes: `PendingEditPlan.Reasoning` (existing), `PendingEditPlan.Edits` (existing, type `[]PendingEditAction`), `PendingEditAction.Tool/Path/OldText/NewText` (existing fields), `relPath(path, cwd)` (existing function), `truncateStr(s, max)` (existing function)
- Produces: Enhanced `formatEditPlanSummary` output that includes file path list and edit previews

- [ ] **Step 1: Write the failing tests**

Add to `engine/turn_editplan_test.go` (append to existing file):

```go
func TestFormatEditPlanSummary_WithEdits(t *testing.T) {
	plan := &PendingEditPlan{
		Reasoning: "问题在 X，方案是改 Y",
		Edits: []PendingEditAction{
			{
				Tool:    "edit",
				Path:    "/cwd/engine/loop.go",
				OldText: "old code here",
				NewText: "new code here",
			},
			{
				Tool:    "write",
				Path:    "/cwd/engine/types.go",
				NewText: "package engine\n",
			},
		},
	}

	t.Run("zh", func(t *testing.T) {
		got := formatEditPlanSummary(plan, true, "/cwd")
		if !strings.Contains(got, "问题在 X，方案是改 Y") {
			t.Errorf("should contain reasoning, got: %s", got)
		}
		if !strings.Contains(got, "涉及 2 个文件的修改") {
			t.Errorf("should show file count, got: %s", got)
		}
		if !strings.Contains(got, "engine/loop.go") {
			t.Errorf("should show first file path, got: %s", got)
		}
		if !strings.Contains(got, "engine/types.go") {
			t.Errorf("should show second file path, got: %s", got)
		}
		if !strings.Contains(got, "old code here") {
			t.Errorf("should show old text preview, got: %s", got)
		}
		if !strings.Contains(got, "new code here") {
			t.Errorf("should show new text preview, got: %s", got)
		}
		if !strings.Contains(got, "确认执行修改？") {
			t.Errorf("should ask for confirmation, got: %s", got)
		}
	})

	t.Run("en", func(t *testing.T) {
		got := formatEditPlanSummary(plan, false, "/cwd")
		if !strings.Contains(got, "2 file(s) to modify") {
			t.Errorf("should show file count in English, got: %s", got)
		}
		if !strings.Contains(got, "Proceed with the changes?") {
			t.Errorf("should ask for confirmation in English, got: %s", got)
		}
	})
}

func TestFormatEditPlanSummary_NoEdits_StillWorks(t *testing.T) {
	// Existing behavior: plan with no edits should still show reasoning + confirmation
	plan := &PendingEditPlan{Reasoning: "some reasoning"}
	got := formatEditPlanSummary(plan, true, "/cwd")
	if !strings.Contains(got, "some reasoning") {
		t.Errorf("should contain reasoning, got: %s", got)
	}
	if !strings.Contains(got, "确认执行修改？") {
		t.Errorf("should ask for confirmation, got: %s", got)
	}
	if strings.Contains(got, "个文件的修改") {
		t.Errorf("should NOT show file count when no edits, got: %s", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/admin/gitspace/deepact && go test ./engine/ -run 'TestFormatEditPlanSummary_WithEdits|TestFormatEditPlanSummary_NoEdits' -v`
Expected: `TestFormatEditPlanSummary_WithEdits` FAILS (output doesn't contain "涉及 2 个文件的修改" or file paths). `TestFormatEditPlanSummary_NoEdits` may PASS (existing behavior).

- [ ] **Step 3: Replace `formatEditPlanSummary`**

In `engine/turn.go`, replace the entire `formatEditPlanSummary` function (lines 1340-1355). Find this exact code:

```go
func formatEditPlanSummary(plan *PendingEditPlan, zh bool, cwd string) string {
	var sb strings.Builder

	// Step 1: Show the reasoning - WHY these changes are proposed.
	if reasoning := plan.Reasoning; reasoning != "" {
		sb.WriteString(reasoning)
		sb.WriteString("\n")
	}
	// Step 2: Ask for confirmation.
	if zh {
		sb.WriteString("\n确认执行修改？")
	} else {
		sb.WriteString("\nProceed with the changes?")
	}
	return sb.String()
}
```

Replace with:

```go
func formatEditPlanSummary(plan *PendingEditPlan, zh bool, cwd string) string {
	var sb strings.Builder

	// Step 1: Show the reasoning - WHY these changes are proposed.
	if reasoning := plan.Reasoning; reasoning != "" {
		sb.WriteString(reasoning)
		sb.WriteString("\n")
	}

	// Step 2: List the files and changes - WHAT will be modified.
	if len(plan.Edits) > 0 {
		if zh {
			sb.WriteString(fmt.Sprintf("\n### 涉及 %d 个文件的修改：\n", len(plan.Edits)))
		} else {
			sb.WriteString(fmt.Sprintf("\n### %d file(s) to modify:\n", len(plan.Edits)))
		}
		for i, edit := range plan.Edits {
			path := relPath(edit.Path, cwd)
			if edit.Tool == "write" {
				if zh {
					sb.WriteString(fmt.Sprintf("%d. **%s** (写入 %d 字符)\n", i+1, path, len(edit.NewText)))
				} else {
					sb.WriteString(fmt.Sprintf("%d. **%s** (write %d chars)\n", i+1, path, len(edit.NewText)))
				}
			} else {
				// edit: show brief old -> new preview
				oldPreview := truncateStr(strings.TrimSpace(edit.OldText), 60)
				newPreview := truncateStr(strings.TrimSpace(edit.NewText), 60)
				sb.WriteString(fmt.Sprintf("%d. **%s**\n   `%s` -> `%s`\n", i+1, path, oldPreview, newPreview))
			}
		}
	}

	// Step 3: Ask for confirmation.
	if zh {
		sb.WriteString("\n确认执行修改？")
	} else {
		sb.WriteString("\nProceed with the changes?")
	}
	return sb.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/admin/gitspace/deepact && go test ./engine/ -run 'TestFormatEditPlanSummary' -v`
Expected: ALL PASS (including existing `TestFormatEditPlanSummary_EmptyReasoningNoAlarmingMessage` and `TestFormatEditPlanSummary_WithReasoning`)

- [ ] **Step 5: Run full test suite**

Run: `cd /Users/admin/gitspace/deepact && go test ./engine/ ./context/ -count=1 -timeout 60s 2>&1 | tail -20`
Expected: All tests pass

- [ ] **Step 6: Commit**

```bash
cd /Users/admin/gitspace/deepact && git add engine/turn.go engine/turn_editplan_test.go && git commit -m "feat: enhance edit plan summary with file paths and edit previews

formatEditPlanSummary now lists each file to be modified with its
relative path and a brief old->new preview, so the user can see
exactly what will change before confirming."
```

---

## Self-Review

### 1. Spec coverage

| Spec requirement | Task |
|------------------|------|
| Component 1: Persistent ANALYSIS MODE constraint | Task 1 (types.go + loop.go intent switch + builder.go injection) |
| Component 2: Analysis report gate | Task 2 (turn.go gate + loop.go confirmation handling) |
| Component 3: Enhanced edit plan summary | Task 3 (turn.go formatEditPlanSummary) |
| Clear AnalysisMode on IntentContinue | Task 1 Step 6 (intent switch) |
| Clear AnalysisMode on IntentNewTopic | Task 1 Step 6 (intent switch) |
| Reset AnalysisReportConfirmed on new topic | Task 1 Step 6 (intent switch) |
| Max 2 gate blocks then degrade | Task 2 Step 6 (`e.analysisNudgeCount < 2`) |
| User confirmation detection via isDangerousConfirmation | Task 2 Step 3 (handleAnalysisNudgeConfirmation) |
| User feedback handling | Task 2 Step 3 (handleAnalysisNudgeConfirmation else branch) |
| Session reset clears all new state | Task 1 Step 7 (clear function) |

No gaps found.

### 2. Placeholder scan

No TBD, TODO, or vague descriptions found. All steps contain complete code.

### 3. Type consistency

- `TaskState.AnalysisMode` — used in Task 1 (types.go definition, loop.go intent switch, builder.go injection, loop.go reset) ✓
- `TaskState.AnalysisReportConfirmed` — used in Task 1 (types.go, loop.go intent switch, loop.go reset) and Task 2 (turn.go gate condition, loop.go confirmation) ✓
- `Engine.pendingAnalysisNudge` — used in Task 1 (loop.go struct, loop.go intent switch reset, loop.go clear function) and Task 2 (turn.go gate sets it, loop.go confirmation reads/clears it) ✓
- `Engine.analysisNudgeCount` — used in Task 1 (loop.go struct, loop.go Run() reset, loop.go clear function) and Task 2 (turn.go gate increments and checks `< 2`) ✓
- `handleAnalysisNudgeConfirmation(userMsg string) bool` — defined in Task 2 Step 3, called in Task 2 Step 4, tested in Task 2 Step 1 ✓
- `formatEditPlanSummary(plan, zh, cwd)` — signature unchanged, only body modified in Task 3 ✓
