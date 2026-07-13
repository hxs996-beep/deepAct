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

func TestHasTrailingNextStepIntent(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		// User's actual case: multi-sentence text ending with next-step intent.
		{"user case", "搜索结果显示项目中确实存在大量关键词匹配业务逻辑的地方。主要集中在 engine/loop.go、engine/roundtable.go 和 engine/dsml.go。让我精读这些关键区域。", true},
		// Single sentence next-step (no prior sentence).
		{"single next step", "让我查看这个文件。", true},
		{"single next step no period", "让我查看这个文件", true},
		// English next step.
		{"english next step", "Search results show matches in loop.go. Let me read these files.", true},
		{"english i'll", "Tests written. I'll run them now.", true},
		// Conclusions - should NOT match.
		{"conclusion", "任务已完成，所有文件已修改。", false},
		{"english conclusion", "All tests pass. Task is complete.", false},
		{"empty", "", false},
		// Summary phrase exclusion: "让我总结" is a conclusion, not next step.
		{"summary exclusion zh", "所有修改已完成。让我总结一下。", false},
		{"summary exclusion en", "Done. Let me summarize the changes.", false},
		// File path period should not split sentence.
		{"file path period", "修改了 engine/loop.go 中的逻辑。", false},
		// Next-step marker not in last sentence -> false (falls to classifier).
		{"marker in earlier sentence", "让我先搜索。搜索完成，共找到 3 处。", false},
		// English sequencing words in conclusions must NOT trigger heuristic.
		// "First,", "Next,", "Then," are adverbs, not intent markers.
		{"english first conclusion", "I fixed the bug. First, I identified the root cause.", false},
		{"english next conclusion", "All done. Next, the task is complete.", false},
		{"english then conclusion", "Tests written. Then, all tests passed.", false},
		// Tier 2: bare action verbs without completion markers (user's 2nd case).
		{"action verb no marker", "发现了多处可能的关键字匹配。深入读取关键位置的代码", true},
		{"action verb continue", "上述分析完成。继续查看更多文件", true},
		{"action verb start", "开始运行测试", true},
		// Tier 2: action verbs WITH completion markers -> not flagged.
		{"action with completion", "运行测试全部通过", false},
		{"action with already", "查看代码后确认问题已修复", false},
		{"action with le", "修改了 engine/loop.go 中的逻辑", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasTrailingNextStepIntent(tt.text); got != tt.want {
				t.Errorf("hasTrailingNextStepIntent(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}
