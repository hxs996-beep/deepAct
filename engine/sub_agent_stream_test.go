package engine

import (
	"testing"
)

func TestSubAgentStreamer_EmitsOnlyOnce(t *testing.T) {
	var calls []ProgressEvent
	fn := func(e ProgressEvent) { calls = append(calls, e) }

	s := subAgentStreamer{}
	s.maybeEmit(fn, "critic", "first content")
	s.maybeEmit(fn, "critic", "second content")
	s.maybeEmit(fn, "critic", "third content")

	if len(calls) != 1 {
		t.Fatalf("expected 1 stream_delta, got %d: %+v", len(calls), calls)
	}
	if calls[0].Type != "stream_delta" {
		t.Errorf("expected type stream_delta, got %q", calls[0].Type)
	}
	if calls[0].Name != "critic" {
		t.Errorf("expected name critic, got %q", calls[0].Name)
	}
	if calls[0].Detail != "first content" {
		t.Errorf("expected first content, got %q", calls[0].Detail)
	}
}

func TestSubAgentStreamer_SkipsEmptyAndNil(t *testing.T) {
	var calls []ProgressEvent
	fn := func(e ProgressEvent) { calls = append(calls, e) }

	s := subAgentStreamer{}
	s.maybeEmit(fn, "critic", "")   // empty content -> no emit, streamed still false
	s.maybeEmit(nil, "critic", "x") // nil onProgress -> no emit, no panic, streamed still false
	s.maybeEmit(fn, "critic", "real content") // first valid -> emit
	s.maybeEmit(fn, "critic", "more")         // already emitted -> no emit

	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %+v", len(calls), calls)
	}
	if calls[0].Detail != "real content" {
		t.Errorf("expected real content, got %q", calls[0].Detail)
	}
}
