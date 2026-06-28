package engine

import (
	"encoding/json"
	"testing"
)

func TestParseReadMultiDigestScopes(t *testing.T) {
	digest := "<!-- read_multi targets: a.go::symbol:Run | b.go::L5-7 | c.txt:: | d.go::L10-end -->\nReadMulti: 4 targets (parallel)"
	recs := parseReadMultiDigestScopes(digest)
	if len(recs) != 4 {
		t.Fatalf("got %d records, want 4", len(recs))
	}
	want := []ReadRecord{
		{Path: "a.go", Scope: "symbol:Run"},
		{Path: "b.go", Scope: "L5-7"},
		{Path: "c.txt", Scope: ""},
		{Path: "d.go", Scope: "L10-end"},
	}
	for i, w := range want {
		if recs[i] != w {
			t.Errorf("rec[%d] = %+v, want %+v", i, recs[i], w)
		}
	}
}

func TestParseReadMultiDigestScopes_NoMetadata(t *testing.T) {
	// No metadata comment → best-effort: return nil, no panic.
	recs := parseReadMultiDigestScopes("just some content without meta")
	if recs != nil {
		t.Fatalf("expected nil for missing metadata, got %v", recs)
	}
}

func TestUpdateTaskState_ReadMultiWritesReadRecords(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	digest := "<!-- read_multi targets: a.go::symbol:Run | b.go::L5-7 -->\nReadMulti: 2 targets (parallel)\n..."
	calls := []ToolCallRequest{{ID: "1", Name: "read_multi", Input: json.RawMessage(`{"targets":[{"path":"a.go"}]}`)}}
	results := []ToolResult{{ToolCallID: "1", ToolName: "read_multi", Status: "ok", Digest: digest}}

	e.updateTaskStateFromTools(calls, results)

	if len(e.state.ReadHistory) != 2 {
		t.Fatalf("ReadHistory len = %d, want 2; records: %+v", len(e.state.ReadHistory), e.state.ReadHistory)
	}
	if e.state.ReadHistory[0] != (ReadRecord{Path: "a.go", Scope: "symbol:Run"}) {
		t.Errorf("rec[0] = %+v", e.state.ReadHistory[0])
	}
	if e.state.ReadHistory[1] != (ReadRecord{Path: "b.go", Scope: "L5-7"}) {
		t.Errorf("rec[1] = %+v", e.state.ReadHistory[1])
	}
}

func TestParseReadMultiTargets(t *testing.T) {
	input := json.RawMessage(`{"targets":[{"path":"a.go","symbol":"Run"},{"path":"b.go","offset":5,"limit":3},{"path":"c.txt"}]}`)
	targets := parseReadMultiTargets(input)
	if len(targets) != 3 {
		t.Fatalf("got %d targets, want 3", len(targets))
	}
	if targets[0].Path != "a.go" || targets[0].Symbol != "Run" {
		t.Errorf("target[0] = %+v", targets[0])
	}
	if targets[1].Offset != 5 || targets[1].Limit != 3 {
		t.Errorf("target[1] = %+v", targets[1])
	}
	if targets[2].Path != "c.txt" {
		t.Errorf("target[2] = %+v", targets[2])
	}
}

func TestLoopGuard_ReadMultiPerTargetBlocks(t *testing.T) {
	// read_multi of the same (path, scope) repeatedly must be blocked by the
	// loop guard, just like repeated single reads — read_multi must not bypass.
	g := NewLoopGuard(3)
	tgt := readMultiTargetView{Path: "a.go", Symbol: "Run"}
	synthInput, _ := json.Marshal(map[string]interface{}{"path": tgt.Path, "symbol": tgt.Symbol, "offset": tgt.Offset, "limit": tgt.Limit})
	synth := ToolCallRequest{ID: "c", Name: "read", Input: synthInput}

	var lastAction GuardAction
	for i := 0; i < 3; i++ {
		lastAction = g.Check(synth)
	}
	if lastAction.Type != GuardBlock {
		t.Fatalf("after 3 repeated read_multi targets, action = %v, want GuardBlock", lastAction.Type)
	}
}
