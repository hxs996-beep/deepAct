package engine

import (
	"testing"
)

func TestSubAgentStreamer_EmitsOnlyOnce(t *testing.T) {
	var calls []ProgressEvent
	fn := func(e ProgressEvent) { calls = append(calls, e) }

	s := subAgentStreamer{}
	s.maybeEmit(fn, "sub", "first content")
	s.maybeEmit(fn, "sub", "second content")
	s.maybeEmit(fn, "sub", "third content")

	if len(calls) != 1 {
		t.Fatalf("expected 1 stream_delta, got %d: %+v", len(calls), calls)
	}
	if calls[0].Type != "stream_delta" {
		t.Errorf("expected type stream_delta, got %q", calls[0].Type)
	}
	if calls[0].Name != "sub" {
		t.Errorf("expected name sub, got %q", calls[0].Name)
	}
	if calls[0].Detail != "first content" {
		t.Errorf("expected first content, got %q", calls[0].Detail)
	}
}

func TestSubAgentStreamer_SkipsEmptyAndNil(t *testing.T) {
	var calls []ProgressEvent
	fn := func(e ProgressEvent) { calls = append(calls, e) }

	s := subAgentStreamer{}
	s.maybeEmit(fn, "sub", "")   // empty content -> no emit, streamed still false
	s.maybeEmit(nil, "sub", "x") // nil onProgress -> no emit, no panic, streamed still false
	s.maybeEmit(fn, "sub", "real content") // first valid -> emit
	s.maybeEmit(fn, "sub", "more")         // already emitted -> no emit

	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %+v", len(calls), calls)
	}
	if calls[0].Detail != "real content" {
		t.Errorf("expected real content, got %q", calls[0].Detail)
	}
}
