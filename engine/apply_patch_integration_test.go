package engine

import (
	"encoding/json"
	"testing"
)

// apply_patch carries no top-level "path" — every file it touches is named by
// an entry in "edits". These tests pin the extraction the task-state
// bookkeeping and the UI summary both rely on.

func TestParseApplyPatchTargets(t *testing.T) {
	input := json.RawMessage(`{"edits":[` +
		`{"path":"a.go"},` +
		`{"path":"a.go","old_string":"x"},` + // duplicate target collapses
		`{"path":"b.go","new_path":"sub/c.go"},` + // move destination included
		`{"file_path":"d.go"}` + // DeepSeek's alias
		`]}`)

	got := parseApplyPatchTargets(input)
	want := []string{"a.go", "b.go", "sub/c.go", "d.go"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("targets[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseApplyPatchTargets_InvalidInput(t *testing.T) {
	if got := parseApplyPatchTargets(json.RawMessage(`{"edits":`)); got != nil {
		t.Fatalf("malformed input should yield nil, got %v", got)
	}
	if got := parseApplyPatchTargets(json.RawMessage(`{}`)); len(got) != 0 {
		t.Fatalf("no edits should yield no targets, got %v", got)
	}
}

// TestUpdateTaskState_ApplyPatchMarksModifiedFiles verifies every file named by
// an apply_patch call lands in ModifiedFiles and the working set. Without this
// the batch's changes are invisible to the task state (modified-file counts,
// compression context, completion checks).
//
// A move counts both paths: the batch is not accounted for unless the source
// (now removed) and the destination (now written) are both recorded.
func TestUpdateTaskState_ApplyPatchMarksModifiedFiles(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	calls := []ToolCallRequest{{
		ID:   "1",
		Name: "apply_patch",
		Input: json.RawMessage(`{"edits":[{"path":"a.go"},{"path":"b.go"},` +
			`{"path":"c.go","new_path":"sub/c.go"}]}`),
	}}
	results := []ToolResult{{ToolCallID: "1", ToolName: "apply_patch", Status: "ok", Digest: "applied 3 edit(s)"}}

	e.updateTaskStateFromTools(calls, results)

	want := []string{"a.go", "b.go", "c.go", "sub/c.go"}
	for _, path := range want {
		if !containsString(e.state.ModifiedFiles, path) {
			t.Errorf("ModifiedFiles = %v, want it to contain %q", e.state.ModifiedFiles, path)
		}
		found := false
		for _, f := range e.state.WorkingSet.Files {
			if f.Path == path && f.Notes == "modified" {
				found = true
			}
		}
		if !found {
			t.Errorf("working set missing %q as modified: %+v", path, e.state.WorkingSet.Files)
		}
	}
	if e.state.EditScopeFiles != len(want) {
		t.Errorf("EditScopeFiles = %d, want %d", e.state.EditScopeFiles, len(want))
	}
}
