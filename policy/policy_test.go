package policy

import (
	"context"
	"testing"

	"github.com/deepact/deepact/engine"
)

// --- AmbiguityDetector ---

func TestNewAmbiguityDetector(t *testing.T) {
	d := NewAmbiguityDetector(0.5)
	if d == nil {
		t.Fatal("expected non-nil AmbiguityDetector")
	}
	if d.Threshold != 0.5 {
		t.Errorf("Threshold = %f, want 0.5", d.Threshold)
	}
}

func TestShouldBlock(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		score     float64
		wantBlock bool
	}{
		{"below threshold", 0.5, 0.3, false},
		{"at threshold", 0.5, 0.5, true},
		{"above threshold", 0.5, 0.8, true},
		{"zero threshold, zero score", 0.0, 0.0, true},
		{"high threshold", 0.9, 0.85, false},
	}
	for _, tt := range tests {
		d := NewAmbiguityDetector(tt.threshold)
		result := engine.AmbiguityResult{Score: tt.score}
		if got := d.ShouldBlock(result); got != tt.wantBlock {
			t.Errorf("%s: ShouldBlock(%v) = %v, want %v", tt.name, result, got, tt.wantBlock)
		}
	}
}

func TestIsChinese(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty", "", false},
		{"english", "hello world", false},
		{"chinese", "你好世界", true},
		{"mixed", "hello 你好 world", true},
		{"numbers", "12345", false},
		{"symbols", "!@#$%", false},
		{"chinese with punctuation", "优化一下代码。", true},
	}
	for _, tt := range tests {
		if got := isChinese(tt.input); got != tt.want {
			t.Errorf("isChinese(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// --- DesignGuard ---

func TestNewDesignGuard(t *testing.T) {
	g := NewDesignGuard()
	if g == nil {
		t.Fatal("expected non-nil DesignGuard")
	}
	if len(g.Patterns) == 0 {
		t.Error("expected at least one anti-pattern")
	}
}

func TestHasBlocking(t *testing.T) {
	tests := []struct {
		name   string
		review engine.DesignReview
		want   bool
	}{
		{"no issues", engine.DesignReview{Verdict: "pass", Issues: nil}, false},
		{"empty issues", engine.DesignReview{Verdict: "pass", Issues: []engine.DesignIssue{}}, false},
		{"warning only", engine.DesignReview{Verdict: "warning", Issues: []engine.DesignIssue{
			{Pattern: "fragile-key", Severity: SeverityWarning, What: "test", Why: "test", Alternative: "test"},
		}}, false},
		{"blocking", engine.DesignReview{Verdict: "blocking", Issues: []engine.DesignIssue{
			{Pattern: "fragile-key", Severity: SeverityBlocking, What: "test", Why: "test", Alternative: "test"},
		}}, true},
		{"mixed", engine.DesignReview{Verdict: "blocking", Issues: []engine.DesignIssue{
			{Pattern: "a", Severity: SeverityWarning, What: "a", Why: "a", Alternative: "a"},
			{Pattern: "b", Severity: SeverityBlocking, What: "b", Why: "b", Alternative: "b"},
		}}, true},
	}
	g := NewDesignGuard()
	for _, tt := range tests {
		if got := g.HasBlocking(tt.review); got != tt.want {
			t.Errorf("%s: HasBlocking() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestHasWarnings(t *testing.T) {
	tests := []struct {
		name   string
		review engine.DesignReview
		want   bool
	}{
		{"no issues", engine.DesignReview{Verdict: "pass", Issues: nil}, false},
		{"empty issues", engine.DesignReview{Verdict: "pass", Issues: []engine.DesignIssue{}}, false},
		{"warning", engine.DesignReview{Verdict: "warning", Issues: []engine.DesignIssue{
			{Pattern: "test", Severity: SeverityWarning},
		}}, true},
		{"blocking only", engine.DesignReview{Verdict: "blocking", Issues: []engine.DesignIssue{
			{Pattern: "test", Severity: SeverityBlocking},
		}}, false},
		{"both", engine.DesignReview{Verdict: "blocking", Issues: []engine.DesignIssue{
			{Pattern: "a", Severity: SeverityWarning},
			{Pattern: "b", Severity: SeverityBlocking},
		}}, true},
	}
	g := NewDesignGuard()
	for _, tt := range tests {
		if got := g.HasWarnings(tt.review); got != tt.want {
			t.Errorf("%s: HasWarnings() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestBuildReviewPrompt(t *testing.T) {
	g := NewDesignGuard()
	prompt := g.BuildReviewPrompt("refactor X to Y", "package main")

	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !contains(t, prompt, "refactor X to Y") {
		t.Error("prompt should contain the plan")
	}
	if !contains(t, prompt, "package main") {
		t.Error("prompt should contain code context")
	}
	if !contains(t, prompt, "fragile-key") {
		t.Error("prompt should contain anti-pattern names")
	}
	if !contains(t, prompt, "verdict") {
		t.Error("prompt should mention JSON output format")
	}
}

func contains(t *testing.T, s, substr string) bool {
	t.Helper()
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// --- Checker ---

func TestNewChecker(t *testing.T) {
	c := NewChecker(0.5)
	if c == nil {
		t.Fatal("expected non-nil Checker")
	}
}

func TestCheckAmbiguity_SkipsWhenGoalSet(t *testing.T) {
	c := NewChecker(0.5)
	state := &engine.TaskState{Goal: "fix the bug"}
	result := c.CheckAmbiguity("vague request", state)
	if result.Score != 0.0 {
		t.Errorf("expected Score=0 when goal set, got %f", result.Score)
	}
}

func TestCheckAmbiguity_SkipsWhenNoModelClient(t *testing.T) {
	c := NewChecker(0.5)
	result := c.CheckAmbiguity("vague request", nil)
	if result.Score != 0.0 {
		t.Errorf("expected Score=0 when no model client, got %f", result.Score)
	}
}

func TestChecker_SetModelClient(t *testing.T) {
	c := NewChecker(0.5)
	c.SetModelClient(&mockModelClient{})
	c.SetModelName("deepseek-v4-flash")
	// No panic — SetModelClient accepts interface
}

func TestCheckScope_NilState(t *testing.T) {
	c := NewChecker(0.5)
	result := c.CheckScope("edit foo.go", nil)
	if !result.Allowed {
		t.Error("expected allowed when state is nil")
	}
}

func TestCheckScope_ConfirmedScope(t *testing.T) {
	c := NewChecker(0.5)
	state := &engine.TaskState{ConfirmedScope: true}
	result := c.CheckScope("edit foo.go", state)
	if !result.Allowed {
		t.Error("expected allowed when scope confirmed")
	}
}

func TestCheckScope_NonDestructiveAction(t *testing.T) {
	c := NewChecker(0.5)
	state := &engine.TaskState{ConfirmedScope: false}
	result := c.CheckScope("grep foo", state)
	if !result.Allowed {
		t.Error("expected allowed for non-destructive action")
	}
}

func TestCheckScope_DestructiveInConfirmedFiles(t *testing.T) {
	c := NewChecker(0.5)
	state := &engine.TaskState{
		ConfirmedScope: false,
		WorkingSet:     engine.WorkingSet{Files: []engine.FileRef{{Path: "foo.go"}}},
	}
	result := c.CheckScope("edit foo.go", state)
	if !result.Allowed {
		t.Error("expected allowed when action matches confirmed file")
	}
}

func TestCheckScope_DestructiveNotInConfirmedFiles(t *testing.T) {
	c := NewChecker(0.5)
	state := &engine.TaskState{
		ConfirmedScope: false,
		WorkingSet:     engine.WorkingSet{Files: []engine.FileRef{{Path: "bar.go"}}},
	}
	result := c.CheckScope("edit foo.go", state)
	if result.Allowed {
		t.Error("expected blocked for destructive action outside confirmed files")
	}
}

func TestCheckScope_DestructiveInModifiedFiles(t *testing.T) {
	c := NewChecker(0.5)
	state := &engine.TaskState{
		ConfirmedScope: false,
		ModifiedFiles:  []string{"foo.go"},
	}
	result := c.CheckScope("edit foo.go", state)
	if !result.Allowed {
		t.Error("expected allowed when action matches modified file")
	}
}

func TestIsDestructiveAction(t *testing.T) {
	tests := []struct {
		action string
		want   bool
	}{
		{"edit config.go", true},
		{"write output.txt", true},
		{"bash ls -la", true},
		{"grep pattern", false},
		{"read file.go", false},
		{"", false},
		{"EDIT config.go", true}, // case insensitive
	}
	for _, tt := range tests {
		if got := isDestructiveAction(tt.action); got != tt.want {
			t.Errorf("isDestructiveAction(%q) = %v, want %v", tt.action, got, tt.want)
		}
	}
}

func TestIsActionInConfirmedFiles(t *testing.T) {
	tests := []struct {
		name   string
		action string
		state  *engine.TaskState
		want   bool
	}{
		{
			name:   "nil state",
			action: "edit foo.go",
			state:  nil,
			want:   false,
		},
		{
			name:   "match working set",
			action: "edit foo.go",
			state:  &engine.TaskState{WorkingSet: engine.WorkingSet{Files: []engine.FileRef{{Path: "foo.go"}}}},
			want:   true,
		},
		{
			name:   "match modified files",
			action: "edit foo.go",
			state:  &engine.TaskState{ModifiedFiles: []string{"foo.go"}},
			want:   true,
		},
		{
			name:   "no match",
			action: "edit bar.go",
			state:  &engine.TaskState{WorkingSet: engine.WorkingSet{Files: []engine.FileRef{{Path: "foo.go"}}}},
			want:   false,
		},
		{
			name:   "case insensitive",
			action: "EDIT FOO.GO",
			state:  &engine.TaskState{WorkingSet: engine.WorkingSet{Files: []engine.FileRef{{Path: "foo.go"}}}},
			want:   true,
		},
	}
	for _, tt := range tests {
		if got := isActionInConfirmedFiles(tt.action, tt.state); got != tt.want {
			t.Errorf("%s: isActionInConfirmedFiles(%q) = %v, want %v", tt.name, tt.action, got, tt.want)
		}
	}
}

func TestParseDesignReview(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantV   string
		wantN   int
	}{
		{
			name:   "plain json",
			input:  `{"verdict":"pass","issues":[]}`,
			wantOK: true, wantV: "pass", wantN: 0,
		},
		{
			name:   "json with issues",
			input:  `{"verdict":"warning","issues":[{"pattern":"fragile-key","severity":"warning","what":"test","why":"test","alternative":"fix it"}]}`,
			wantOK: true, wantV: "warning", wantN: 1,
		},
		{
			name:   "wrapped in markdown code block",
			input:  "```json\n{\"verdict\":\"pass\",\"issues\":[]}\n```",
			wantOK: true, wantV: "pass", wantN: 0,
		},
		{
			name:   "blocking verdict empty fallback",
			input:  `{"issues":[]}`,
			wantOK: true, wantV: "pass", wantN: 0,
		},
		{
			name:   "invalid json",
			input:  `not json at all`,
			wantOK: false, wantV: "pass", wantN: 0,
		},
	}
	for _, tt := range tests {
		review, err := parseDesignReview(tt.input)
		if (err == nil) != tt.wantOK {
			t.Errorf("%s: parseDesignReview() err = %v, wantOK=%v", tt.name, err, tt.wantOK)
		}
		if review.Verdict != tt.wantV {
			t.Errorf("%s: Verdict = %q, want %q", tt.name, review.Verdict, tt.wantV)
		}
		if len(review.Issues) != tt.wantN {
			t.Errorf("%s: len(Issues) = %d, want %d", tt.name, len(review.Issues), tt.wantN)
		}
	}
}

// mockModelClient implements engine.ModelClient for tests that need a non-nil client.
type mockModelClient struct{}

func (m *mockModelClient) Stream(ctx context.Context, req engine.ModelRequest) (<-chan engine.ModelChunk, error) {
	ch := make(chan engine.ModelChunk)
	close(ch)
	return ch, nil
}

func (m *mockModelClient) Complete(ctx context.Context, req engine.ModelRequest) (*engine.ModelResponse, error) {
	return &engine.ModelResponse{
		Message: engine.ModelMessage{Content: `{"score":0.0,"questions":[]}`},
	}, nil
}
