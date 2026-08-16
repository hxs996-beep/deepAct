package engine

import (
	"context"
	"strings"
	"testing"
)

// TestExecuteTurn_EmitsContentDelta verifies that content_delta ProgressEvents
// are emitted for streamed text, with the correct Detail. Short unbroken text
// (no blank line) is coalesced into a single complete event.
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
	// 无空行短文本：合并为一个事件，Detail 等于拼接全文（不再逐 chunk 发射）。
	if len(contentDeltas) != 1 {
		t.Fatalf("expected 1 content_delta event, got %d: %+v", len(contentDeltas), contentDeltas)
	}
	if contentDeltas[0].Detail != "让我搜索这个函数的定义" {
		t.Errorf("delta Detail = %q, want %q", contentDeltas[0].Detail, "让我搜索这个函数的定义")
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

// TestExecuteTurn_SegmentsOnBlankLine verifies a blank line (\n\n) is a
// paragraph boundary: content before it is emitted as one complete event
// (including the \n\n), and the next paragraph stays buffered until its own
// boundary or stream end.
func TestExecuteTurn_SegmentsOnBlankLine(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "第一段。"},
			{Delta: "\n\n第二段。"},
			{Delta: "\n\n"},
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

	var deltas []string
	for _, ev := range events {
		if ev.Type == "content_delta" {
			deltas = append(deltas, ev.Detail)
		}
	}
	want := []string{"第一段。\n\n", "第二段。\n\n"}
	if len(deltas) != len(want) {
		t.Fatalf("expected %d content_delta events, got %d: %+v", len(want), len(deltas), deltas)
	}
	for i := range want {
		if deltas[i] != want[i] {
			t.Errorf("delta[%d] = %q, want %q", i, deltas[i], want[i])
		}
	}
}

// TestExecuteTurn_ParagraphSpansChunks verifies a paragraph split across two
// chunks is emitted ONCE, only after its \n\n boundary arrives.
func TestExecuteTurn_ParagraphSpansChunks(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "第一"},
			{Delta: "段\n\n"},
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

	var deltas []string
	for _, ev := range events {
		if ev.Type == "content_delta" {
			deltas = append(deltas, ev.Detail)
		}
	}
	if len(deltas) != 1 {
		t.Fatalf("expected 1 content_delta event, got %d: %+v", len(deltas), deltas)
	}
	if deltas[0] != "第一段\n\n" {
		t.Errorf("delta[0] = %q, want %q", deltas[0], "第一段\n\n")
	}
}

// TestExecuteTurn_ForceFlushLongParagraph verifies a long paragraph with no
// blank line is force-flushed as soon as the buffer reaches maxSegmentRunes,
// so the UI never waits on an unbroken paragraph. Rejoining all events must
// equal the original text (no rune lost).
func TestExecuteTurn_ForceFlushLongParagraph(t *testing.T) {
	chunk := strings.Repeat("测", 30) // 30 runes per chunk
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: chunk},
			{Delta: chunk},
			{Delta: chunk},
			{Delta: chunk},
			{Delta: chunk},
			{Delta: chunk},
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

	var deltas []string
	for _, ev := range events {
		if ev.Type == "content_delta" {
			deltas = append(deltas, ev.Detail)
		}
	}
	// 30*2=60 触发第一次切段，之后每 60 字触发一次：180 字 → 3 个事件。
	if len(deltas) != 3 {
		t.Fatalf("expected 3 content_delta events for 180 runes, got %d: %+v", len(deltas), deltas)
	}
	joined := strings.Join(deltas, "")
	if joined != strings.Repeat("测", 180) {
		t.Errorf("rejoined text = %q (len %d), want %d runes of 测", joined, len(joined), 180)
	}
}
