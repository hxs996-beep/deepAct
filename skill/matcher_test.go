package skill

import (
	"context"
	"testing"
	"time"
)

func TestKeywordMatcher_Match(t *testing.T) {
	r := NewRegistry()
	r.Register(&Skill{
		Name:                  "systematic-debugging",
		Description:           "Use when encountering any bug",
		Keywords:              []string{"debug", "bug", "调试"},
		AutoActivateThreshold: intPtr(1),
	})
	r.Register(&Skill{
		Name:                  "brainstorming",
		Description:           "Use before any creative work",
		Keywords:              []string{"设计", "design"},
		AutoActivateThreshold: intPtr(1),
	})

	m := NewKeywordMatcher(r)

	// Match: 关键词命中 + 阈值达标
	got := m.Match(context.Background(), "有个bug需要调试", r.All())
	if got == nil || got.Name != "systematic-debugging" {
		t.Fatalf("expected systematic-debugging, got %v", got)
	}

	// No match: 关键词未命中
	got = m.Match(context.Background(), "帮我写个排序算法", r.All())
	if got != nil {
		t.Fatalf("expected nil, got %v", got.Name)
	}
}

func TestKeywordMatcher_NoAutoActivateThreshold(t *testing.T) {
	r := NewRegistry()
	r.Register(&Skill{
		Name:     "systematic-debugging",
		Keywords: []string{"debug", "bug"},
		// AutoActivateThreshold is nil — never auto-activate
	})

	m := NewKeywordMatcher(r)
	got := m.Match(context.Background(), "有个bug", r.All())
	if got != nil {
		t.Fatalf("expected nil (no threshold), got %v", got.Name)
	}
}

func intPtr(i int) *int { return &i }

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
		t.Fatalf("expected nil on timeout, got %v", got)
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

func TestFallbackMatcher_KeywordFirst(t *testing.T) {
	r := NewRegistry()
	r.Register(&Skill{
		Name:                  "systematic-debugging",
		Keywords:              []string{"debug", "bug"},
		AutoActivateThreshold: intPtr(1),
	})

	kw := NewKeywordMatcher(r)
	// semantic should never be called — verify by making it return a different skill
	semCalled := false
	sem := NewSemanticMatcher(func(ctx context.Context, sys, usr string) (string, error) {
		semCalled = true
		return `{"skill": "brainstorming"}`, nil
	}, "test-model")

	fb := NewFallbackMatcher(kw, sem)
	got := fb.Match(context.Background(), "有个bug", r.All())
	if got == nil || got.Name != "systematic-debugging" {
		t.Fatalf("expected systematic-debugging, got %v", got)
	}
	if semCalled {
		t.Fatal("semantic matcher should not have been called when keyword matches")
	}
}

func TestFallbackMatcher_SemanticFallback(t *testing.T) {
	r := NewRegistry()
	r.Register(&Skill{
		Name:        "systematic-debugging",
		Keywords:    []string{"debug"},
		AutoActivateThreshold: intPtr(1),
	})
	r.Register(&Skill{
		Name:        "brainstorming",
		Keywords:    []string{"design"},
		AutoActivateThreshold: intPtr(1),
	})

	kw := NewKeywordMatcher(r)
	sem := NewSemanticMatcher(func(ctx context.Context, sys, usr string) (string, error) {
		return `{"skill": "systematic-debugging"}`, nil
	}, "test-model")

	fb := NewFallbackMatcher(kw, sem)
	got := fb.Match(context.Background(), "渲染问题需要分析", r.All())
	if got == nil || got.Name != "systematic-debugging" {
		t.Fatalf("expected systematic-debugging via semantic fallback, got %v", got)
	}
}

func TestFallbackMatcher_SemanticDisabled(t *testing.T) {
	r := NewRegistry()
	r.Register(&Skill{Name: "debug", Keywords: []string{"debug"}, AutoActivateThreshold: intPtr(1)})

	kw := NewKeywordMatcher(r)
	sem := NewSemanticMatcher(nil, "") // disabled

	fb := NewFallbackMatcher(kw, sem)
	got := fb.Match(context.Background(), "渲染问题需要分析", r.All())
	if got != nil {
		t.Fatalf("expected nil when keyword misses and semantic disabled, got %v", got.Name)
	}
}
