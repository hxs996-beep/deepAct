package skill

import (
	"context"
	"testing"
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
