package engine

import (
	"strings"
	"testing"
)

// buildSubHistory constructs a history slice for compressSubHistory tests:
// [system, first-user] + nTurns turns, each turn = assistant(tool_call) + tool
// result with body of length bodyLen. Older turns carry increasingly large
// results so truncation is observable.
func buildSubHistory(nTurns, bodyLen int) []ModelMessage {
	h := []ModelMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "first user"},
	}
	body := strings.Repeat("x", bodyLen)
	for i := 0; i < nTurns; i++ {
		h = append(h, ModelMessage{
			Role: "assistant",
			ToolCalls: []ModelToolCall{{
				ID: "call", Type: "function",
				Function: ModelFunctionCall{Name: "read", Arguments: "{}"},
			}},
		})
		h = append(h, ModelMessage{
			Role:       "tool",
			ToolCallID: "call",
			Content:    body,
		})
	}
	return h
}

func TestCompressSubHistoryKeepsStablePrefixAndFreshTurns(t *testing.T) {
	// 25 turns total: 20 fresh (verbatim) + 5 stale (truncated).
	h := buildSubHistory(25, 1000)
	compressed := compressSubHistory(h)

	if len(compressed) < 2 {
		t.Fatalf("compressed history lost the stable prefix: len=%d", len(compressed))
	}
	if compressed[0].Content != "system" || compressed[1].Content != "first user" {
		t.Fatalf("stable prefix modified: got %q, %q", compressed[0].Content, compressed[1].Content)
	}

	// The last 20 turns (indices >= 5) must be verbatim — tool results intact.
	// History layout: [sys, user] + 50 msgs (25 turns × 2). Fresh starts at msg
	// index 2+ (25-20)*2 = 12; its tool results must be untruncated.
	for i := 12; i < len(compressed); i++ {
		if compressed[i].Role == "tool" && len(compressed[i].Content) != 1000 {
			t.Fatalf("fresh turn tool result truncated at idx %d: len=%d", i, len(compressed[i].Content))
		}
	}

	// Stale turns (5 oldest, msg indices 2..11) keep assistant + tool_call_id,
	// but tool bodies are cut to the cap.
	truncated := 0
	for i := 2; i < 12; i++ {
		msg := compressed[i]
		if msg.Role == "tool" {
			if msg.ToolCallID != "call" {
				t.Fatalf("stale tool message lost tool_call_id at idx %d", i)
			}
			// Body truncated to cap + marker (~51 bytes); anything ≥ original
			// length (1000) means truncation never applied.
			if len(msg.Content) > subAgentToolResultCap+60 {
				t.Fatalf("stale tool result not truncated at idx %d: len=%d", i, len(msg.Content))
			}
			if !strings.Contains(msg.Content, "truncated") {
				t.Fatalf("stale tool result missing truncation marker at idx %d", i)
			}
			truncated++
		}
	}
	if truncated == 0 {
		t.Fatal("expected stale tool results to be truncated")
	}
	// Ordering preserved: stale tool messages still exist after their assistant.
	for i := 3; i < 12; i += 2 {
		if compressed[i-1].Role != "assistant" || compressed[i].Role != "tool" {
			t.Fatalf("turn structure broken at idx %d: %q → %q", i, compressed[i-1].Role, compressed[i].Role)
		}
	}
}

func TestCompressSubHistorySmallHistoryUnchanged(t *testing.T) {
	// Fewer turns than keepTurns: nothing truncated.
	h := buildSubHistory(3, 5000)
	compressed := compressSubHistory(h)
	if len(compressed) != len(h) {
		t.Fatalf("small history changed length: %d → %d", len(h), len(compressed))
	}
	for i := range h {
		if compressed[i].Content != h[i].Content {
			t.Fatalf("small history content changed at idx %d", i)
		}
	}
}

func TestCompressSubHistoryPreservesOrder(t *testing.T) {
	h := buildSubHistory(30, 800)
	compressed := compressSubHistory(h)
	// Every assistant must be immediately followed by its tool message.
	for i := 1; i < len(compressed); i++ {
		if compressed[i-1].Role == "assistant" && compressed[i].Role != "tool" {
			t.Fatalf("assistant at idx %d not followed by tool", i-1)
		}
	}
}

func TestTruncateToolResultUnderCapUnchanged(t *testing.T) {
	msg := ModelMessage{Role: "tool", ToolCallID: "c1", Content: "short"}
	got := truncateToolResult(msg)
	if got.Content != "short" {
		t.Fatalf("under-cap message changed: %q", got.Content)
	}
	if got.ToolCallID != "c1" {
		t.Fatalf("under-cap message lost tool_call_id")
	}
}

func TestTruncateToolResultOverCap(t *testing.T) {
	msg := ModelMessage{Role: "tool", ToolCallID: "c2", Content: strings.Repeat("y", 2000)}
	got := truncateToolResult(msg)
	if len(got.Content) > subAgentToolResultCap+60 {
		t.Fatalf("over-cap message not truncated: len=%d", len(got.Content))
	}
	if !strings.Contains(got.Content, "truncated") {
		t.Fatalf("truncation marker missing: %q", got.Content)
	}
	if got.ToolCallID != "c2" {
		t.Fatalf("truncation lost tool_call_id")
	}
}
