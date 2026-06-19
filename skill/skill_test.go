package skill

import (
	"testing"
)

func TestMatchByKeywords_FindsMatchingSkill(t *testing.T) {
	r := NewRegistry()
	r.Register(&Skill{
		Name:        "test-driven-development",
		Description: "Write test first, then implement",
		Keywords:    []string{"测试", "test", "tdd", "TDD"},
	})
	r.Register(&Skill{
		Name:        "code-review",
		Description: "Review code for issues",
		Keywords:    []string{"review", "评审", "code review"},
	})

	tests := []struct {
		name     string
		input    string
		wantName string // empty means expect nil
	}{
		{
			name:     "matches tdd keyword",
			input:    "帮我用TDD方式实现这个功能",
			wantName: "test-driven-development",
		},
		{
			name:     "matches review keyword",
			input:    "帮我评审这段代码",
			wantName: "code-review",
		},
		{
			name:     "matches english tdd keyword",
			input:    "let's do tdd for this feature",
			wantName: "test-driven-development",
		},
		{
			name:     "no match returns nil",
			input:    "帮我写一个排序算法",
			wantName: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.MatchByKeywords(tt.input)
			if tt.wantName == "" {
				if got != nil {
					t.Errorf("MatchByKeywords(%q) = %v, want nil", tt.input, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("MatchByKeywords(%q) = nil, want %q", tt.input, tt.wantName)
			}
			if got.Name != tt.wantName {
				t.Errorf("MatchByKeywords(%q).Name = %q, want %q", tt.input, got.Name, tt.wantName)
			}
		})
	}
}

func TestMatchByKeywords_BestMatchWins(t *testing.T) {
	r := NewRegistry()
	r.Register(&Skill{
		Name:     "systematic-debugging",
		Keywords: []string{"debug", "bug", "调试"},
	})
	r.Register(&Skill{
		Name:     "code-review",
		Keywords: []string{"review", "评审", "code review"},
	})

	tests := []struct {
		name     string
		input    string
		wantName string
	}{
		{
			name:     "debug keyword matches debugging",
			input:    "有个bug需要调试一下",
			wantName: "systematic-debugging",
		},
		{
			name:     "review keyword matches review",
			input:    "please review my code",
			wantName: "code-review",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.MatchByKeywords(tt.input)
			if got == nil {
				t.Fatalf("MatchByKeywords(%q) = nil, want %q", tt.input, tt.wantName)
			}
			if got.Name != tt.wantName {
				t.Errorf("MatchByKeywords(%q).Name = %q, want %q", tt.input, got.Name, tt.wantName)
			}
		})
	}
}
