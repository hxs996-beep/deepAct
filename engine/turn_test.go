package engine

import (
	"encoding/json"
	"testing"
)

func TestExtractReadScope(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare read", `{"path":"a.go"}`, ""},
		{"symbol", `{"path":"a.go","symbol":"Run"}`, "symbol:Run"},
		{"offset+limit", `{"path":"a.go","offset":10,"limit":50}`, "L10-50"},
		{"offset only", `{"path":"a.go","offset":10}`, "L10-"},
		{"limit only", `{"path":"a.go","limit":50}`, "L1-50"},
		{"empty input", ``, ""},
		{"invalid json", `{not json`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractReadScope(json.RawMessage(tt.input))
			if got != tt.want {
				t.Errorf("extractReadScope(%s) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestUpdateTaskStateFromTools_RecordsReadHistory(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	calls := []ToolCallRequest{
		{Name: "read", Input: json.RawMessage(`{"path":"a.go","symbol":"Run"}`)},
		{Name: "read", Input: json.RawMessage(`{"path":"a.go","offset":10,"limit":50}`)},
		{Name: "read", Input: json.RawMessage(`{"path":"b.go"}`)},
	}
	e.updateTaskStateFromTools(calls, nil)
	want := []ReadRecord{
		{Path: "a.go", Scope: "symbol:Run"},
		{Path: "a.go", Scope: "L10-50"},
		{Path: "b.go", Scope: ""},
	}
	if len(e.state.ReadHistory) != len(want) {
		t.Fatalf("got %d records, want %d: %+v", len(e.state.ReadHistory), len(want), e.state.ReadHistory)
	}
	for i, r := range want {
		if e.state.ReadHistory[i] != r {
			t.Errorf("record %d = %+v, want %+v", i, e.state.ReadHistory[i], r)
		}
	}
}
