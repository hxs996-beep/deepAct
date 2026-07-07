package engine

import (
	"context"
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

// stubStreamModel emits pre-configured chunks then closes the channel.
type stubStreamModel struct {
	chunks []ModelChunk
}

func (m *stubStreamModel) Stream(_ context.Context, _ ModelRequest) (<-chan ModelChunk, error) {
	ch := make(chan ModelChunk, len(m.chunks))
	for _, c := range m.chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func (m *stubStreamModel) Complete(_ context.Context, _ ModelRequest) (*ModelResponse, error) {
	return &ModelResponse{}, nil
}

// stubToolExecutor is a minimal ToolExecutor for testing executeTurn.
type stubToolExecutor struct{}

func (stubToolExecutor) Execute(_ ToolExecContext, _ []ToolCallRequest) []ToolResult { return nil }
func (stubToolExecutor) Specs() []ModelTool                                           { return nil }

// TestExecuteTurn_ZeroToolCalls_StopHookNudges verifies that when
// the model emits text without a tool call and runToolCallCount=0,
// the stop hook injects a nudge and continues the loop.
func TestExecuteTurn_ZeroToolCalls_StopHookNudges(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "查看 buildResult 如何提取 Summary", FinishReason: "stop"},
		}},
		context:   &stubContextBuilder{},
		tools:     stubToolExecutor{},
		state:     &TaskState{TurnNumber: 0},
		history:   []Message{{Role: "user", Content: "执行方案"}},
		config:    EngineConfig{ModelName: "test-model"},
		stopHooks: []StopHook{&ZeroToolCallHook{MaxRetries: 3}},
		isChinese: true,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Done {
		t.Errorf("expected Done=false (zero tool calls → stop hook should nudge), got Done=true")
	}
	last := e.history[len(e.history)-1]
	if last.Role != "user" {
		t.Errorf("expected last message to be user nudge, got role=%q", last.Role)
	}
	if result.FinishReason != "stop" {
		t.Errorf("expected FinishReason='stop', got %q", result.FinishReason)
	}
}

// TestExecuteTurn_FinalTextAfterToolCalls_Done verifies that a
// conclusion after prior tool calls ends the loop (stop hook won't block
// when runToolCallCount > 0).
func TestExecuteTurn_FinalTextAfterToolCalls_Done(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成，所有文件已修改。", FinishReason: "stop"},
		}},
		context:          &stubContextBuilder{},
		tools:            stubToolExecutor{},
		state:            &TaskState{TurnNumber: 0},
		history:          []Message{{Role: "user", Content: "执行方案"}},
		config:           EngineConfig{ModelName: "test-model"},
		stopHooks:        []StopHook{&ZeroToolCallHook{MaxRetries: 3}},
		isChinese:        true,
		runToolCallCount: 2, // prior tool calls → hook won't block
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Done {
		t.Errorf("expected Done=true for final conclusion text, got Done=false")
	}
}
