package engine

import (
	"context"
	"testing"
)

// TestExecuteTurn_EmitsContentDelta verifies that content_delta ProgressEvents
// are emitted during streaming, one per chunk.Delta, with the correct Detail.
func TestExecuteTurn_EmitsContentDelta(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "让我搜索这个函数"},
			{Delta: "的定义"},
			{FinishReason: "stop"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "查找函数"}},
		config: EngineConfig{
			ModelName: "test-model",
			OnProgress: func(ev ProgressEvent) {
				events = append(events, ev)
			},
		},
		isChinese: true,
	}

	_, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}

	var contentDeltas []ProgressEvent
	for _, ev := range events {
		if ev.Type == "content_delta" {
			contentDeltas = append(contentDeltas, ev)
		}
	}
	if len(contentDeltas) != 2 {
		t.Fatalf("expected 2 content_delta events, got %d: %+v", len(contentDeltas), contentDeltas)
	}
	if contentDeltas[0].Detail != "让我搜索这个函数" {
		t.Errorf("first delta Detail = %q, want %q", contentDeltas[0].Detail, "让我搜索这个函数")
	}
	if contentDeltas[1].Detail != "的定义" {
		t.Errorf("second delta Detail = %q, want %q", contentDeltas[1].Detail, "的定义")
	}
}

// TestExecuteTurn_NoContentDeltaForEmptyChunk verifies that empty Delta chunks
// do not produce content_delta events.
func TestExecuteTurn_NoContentDeltaForEmptyChunk(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: ""},
			{ReasoningDelta: "thinking..."},
			{Delta: "real content"},
			{FinishReason: "stop"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "test"}},
		config: EngineConfig{
			ModelName: "test-model",
			OnProgress: func(ev ProgressEvent) {
				events = append(events, ev)
			},
		},
		isChinese: true,
	}

	_, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}

	var count int
	for _, ev := range events {
		if ev.Type == "content_delta" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 content_delta event (empty Delta skipped), got %d", count)
	}
}
