package context

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/deepact/deepact/engine"
)

func TestFormatTaskStateVolatile_IncludesReadHistory(t *testing.T) {
	state := &engine.TaskState{
		TurnNumber: 3,
		ReadHistory: []engine.ReadRecord{
			{Path: "a.go", Scope: "symbol:Run"},
			{Path: "a.go", Scope: "L10-50"},
			{Path: "b.go", Scope: ""},
		},
	}
	out := formatTaskStateVolatile(state)
	if out == "" {
		t.Fatal("expected non-empty volatile output")
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	rh, ok := m["read_history"]
	if !ok {
		t.Fatal("read_history missing from volatile state")
	}
	s := fmt.Sprintf("%v", rh)
	if !strings.Contains(s, "a.go") || !strings.Contains(s, "b.go") {
		t.Errorf("read_history does not contain expected paths: %v", rh)
	}
}

// TestFlattenReadHistory_NoTruncation confirms the old 20-record cap is gone:
// all distinct (path, scope) reads are preserved, and duplicates collapse.
func TestFlattenReadHistory_NoTruncation(t *testing.T) {
	// 30 distinct reads — would have been truncated to 20 before.
	recs := make([]engine.ReadRecord, 0, 30)
	for i := 0; i < 30; i++ {
		recs = append(recs, engine.ReadRecord{Path: fmt.Sprintf("file%02d.go", i), Scope: ""})
	}
	// Plus a duplicate that should collapse.
	recs = append(recs, engine.ReadRecord{Path: "file00.go", Scope: ""})

	out := flattenReadHistory(recs)
	if len(out) != 30 {
		t.Errorf("expected 30 distinct records (no truncation, dedup of 1 dup), got %d", len(out))
	}
	// file00.go should appear exactly once.
	count := 0
	for _, r := range out {
		if r.Path == "file00.go" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected file00.go once after dedup, got %d", count)
	}
}

func TestBuildReadHistoryHint(t *testing.T) {
	records := []engine.ReadRecord{
		{Path: "a.go", Scope: "symbol:Run"},
		{Path: "a.go", Scope: "L10-50"},
		{Path: "b.go", Scope: ""},
	}
	out := BuildReadHistoryHint(records, "zh")
	if !strings.Contains(out, "a.go") || !strings.Contains(out, "b.go") {
		t.Errorf("hint missing paths: %s", out)
	}
	if !strings.Contains(out, "不要重读") {
		t.Errorf("hint missing do-not-reread directive: %s", out)
	}
	// Dedup: a.go appears once despite two records.
	if c := strings.Count(out, "a.go"); c != 1 {
		t.Errorf("a.go should appear once (deduped), got %d: %s", c, out)
	}
}

func TestBuildReadHistoryHint_Empty(t *testing.T) {
	if out := BuildReadHistoryHint(nil, "zh"); out != "" {
		t.Errorf("empty input should return empty, got %q", out)
	}
}
