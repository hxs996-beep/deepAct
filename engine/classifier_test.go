package engine

import (
	"testing"
)

func TestExtractRememberMarkers(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantN    int
		wantText string // first marker text if wantN > 0
	}{
		{
			name:    "no markers",
			content: "plain text without any markers",
			wantN:   0,
		},
		{
			name:     "single marker",
			content:  `some text <!-- REMEMBER: key file is config.go --> more text`,
			wantN:    1,
			wantText: "key file is config.go",
		},
		{
			name:     "multiple markers",
			content:  `<!-- REMEMBER: first --> and <!-- REMEMBER: second -->`,
			wantN:    2,
			wantText: "first",
		},
		{
			name:     "duplicate markers deduped",
			content:  `<!-- REMEMBER: same --> and <!-- REMEMBER: same -->`,
			wantN:    1,
			wantText: "same",
		},
		{
			name:     "empty marker content",
			content:  `<!-- REMEMBER:  -->`,
			wantN:    0,
		},
		{
			name:    "malformed marker",
			content: `<!-- REMEMBER missing close`,
			wantN:   0,
		},
		{
			name:     "stripped whitespace",
			content:  `<!--  REMEMBER:   trim me   -->`,
			wantN:    1,
			wantText: "trim me",
		},
	}
	for _, tt := range tests {
		markers := extractRememberMarkers(tt.content)
		if len(markers) != tt.wantN {
			t.Errorf("%s: got %d markers, want %d", tt.name, len(markers), tt.wantN)
			continue
		}
		if tt.wantN > 0 && markers[0] != tt.wantText {
			t.Errorf("%s: first marker = %q, want %q", tt.name, markers[0], tt.wantText)
		}
	}
}

func TestExtractRememberMarkers_NilOnNoMatch(t *testing.T) {
	result := extractRememberMarkers("nothing here")
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestIsIntermediateText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", false},
		{"dots only", "...", false},
		{"let me english", "Let me check the file first", true},
		{"let me lowercase start", "let me verify the result", false},  // case-sensitive
		{"chinese let me", "让我先读取代码", true},
		{"chinese i will", "我来处理这个问题", true},
		{"chinese i need to first", "我要先确认一下", true},
		{"chinese next", "接下来我们看看", true},
		{"chinese first i will", "我先读取文件", true},
		{"normal content", "The file contains a bug at line 42", false},
		{"code output", "package main\nfunc main() {}", false},
		// Long text with a leading "让我" but carrying a real conclusion must
		// NOT be discarded — this is the bug that produced empty "完成" output.
		{"long text with conclusion", "让我读取文件后，发现 bug 在第 42 行，建议在 turn.go 中修复该问题", false},
		// Marker not at start → keep (not pure intent).
		{"marker mid-sentence", "文件已读取。让我看看下一步。", false},
	}
	for _, tt := range tests {
		if got := isIntermediateText(tt.text); got != tt.want {
			t.Errorf("%s: isIntermediateText(%q) = %v, want %v", tt.name, tt.text, got, tt.want)
		}
	}
}
