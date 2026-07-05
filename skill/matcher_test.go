package skill

import (
	"context"
	"testing"
	"time"
)

func TestSemanticMatcher_Match(t *testing.T) {
	r := NewRegistry()
	r.Register(&Skill{
		Name:        "systematic-debugging",
		Description: "Use when encountering any bug, test failure, or unexpected behavior",
	})

	// Mock LLM returns valid JSON
	mockFn := func(ctx context.Context, systemMsg, userMsg string) (string, error) {
		return `{"skill": "systematic-debugging"}`, nil
	}
	m := NewSemanticMatcher(mockFn, "test-model")
	got := m.Match(context.Background(), "渲染问题需要分析", r.All())
	if got == nil || got.Name != "systematic-debugging" {
		t.Fatalf("expected systematic-debugging, got %v", got)
	}
}

func TestSemanticMatcher_NoMatch(t *testing.T) {
	r := NewRegistry()
	r.Register(&Skill{Name: "debug", Description: "debug stuff"})

	mockFn := func(ctx context.Context, systemMsg, userMsg string) (string, error) {
		return `{"skill": null}`, nil
	}
	m := NewSemanticMatcher(mockFn, "test-model")
	got := m.Match(context.Background(), "hello world", r.All())
	if got != nil {
		t.Fatalf("expected nil, got %v", got.Name)
	}
}

func TestSemanticMatcher_Timeout(t *testing.T) {
	r := NewRegistry()
	r.Register(&Skill{Name: "debug", Description: "debug"})

	mockFn := func(ctx context.Context, systemMsg, userMsg string) (string, error) {
		// Simulate timeout via context cancellation
		<-ctx.Done()
		return "", ctx.Err()
	}
	m := NewSemanticMatcher(mockFn, "test-model")
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	got := m.Match(ctx, "test", r.All())
	if got != nil {
		t.Fatalf("expected nil on timeout, got %v", got.Name)
	}
}

func TestSemanticMatcher_BadJSON(t *testing.T) {
	r := NewRegistry()
	r.Register(&Skill{Name: "debug", Description: "debug"})

	mockFn := func(ctx context.Context, systemMsg, userMsg string) (string, error) {
		return `not json at all`, nil
	}
	m := NewSemanticMatcher(mockFn, "test-model")
	got := m.Match(context.Background(), "test", r.All())
	if got != nil {
		t.Fatalf("expected nil on bad JSON, got %v", got.Name)
	}
}

func TestSemanticMatcher_UnknownSkill(t *testing.T) {
	r := NewRegistry()
	r.Register(&Skill{Name: "debug", Description: "debug"})

	mockFn := func(ctx context.Context, systemMsg, userMsg string) (string, error) {
		return `{"skill": "nonexistent-skill"}`, nil
	}
	m := NewSemanticMatcher(mockFn, "test-model")
	got := m.Match(context.Background(), "test", r.All())
	if got != nil {
		t.Fatalf("expected nil for unknown skill, got %v", got.Name)
	}
}

func TestSemanticMatcher_EmptyModelName(t *testing.T) {
	r := NewRegistry()
	m := NewSemanticMatcher(nil, "") // empty model name disables
	got := m.Match(context.Background(), "test", r.All())
	if got != nil {
		t.Fatalf("expected nil when disabled, got %v", got.Name)
	}
}
