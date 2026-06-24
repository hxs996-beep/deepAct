package engine

import (
	"encoding/json"
	"testing"
	"time"
)

// --- CompressionOrchestrator ---

func TestNewCompressionOrchestrator(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "deepseek-v4-pro")
	if c == nil {
		t.Fatal("expected non-nil CompressionOrchestrator")
	}
	if c.flashModelName != "deepseek-v4-flash" {
		t.Errorf("flashModelName = %q, want deepseek-v4-flash", c.flashModelName)
	}
}

func TestSetFlashModelName(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "pro")
	c.SetFlashModelName("custom-flash")
	if c.flashModelName != "custom-flash" {
		t.Errorf("flashModelName = %q, want 'custom-flash'", c.flashModelName)
	}
}

func TestShouldCompress(t *testing.T) {
	tests := []struct {
		name     string
		current  int
		max      int
		wantLayer CompressionLayer
		wantOk   bool
	}{
		{"zero max", 1000, 0, LayerToolGovernance, false},
		{"negative max", 1000, -1, LayerToolGovernance, false},
		{"below threshold", 100, 1000, LayerToolGovernance, false},
		{"at threshold", 800, 1000, LayerFullCompact, true},
		{"above threshold", 900, 1000, LayerFullCompact, true},
		{"exact 80%", 800, 1000, LayerFullCompact, true},
	}
	c := NewCompressionOrchestrator(nil, nil, "pro")
	for _, tt := range tests {
		layer, ok := c.ShouldCompress(tt.current, tt.max)
		if layer != tt.wantLayer || ok != tt.wantOk {
			t.Errorf("%s: ShouldCompress(%d,%d) = (%v,%v), want (%v,%v)",
				tt.name, tt.current, tt.max, layer, ok, tt.wantLayer, tt.wantOk)
		}
	}
}

func TestCompress_NoOpOnToolGovernance(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "pro")
	history := []Message{{Role: "user", Content: "hello"}}
	result, err := c.Compress(LayerToolGovernance, nil, history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected unchanged history length, got %d", len(result))
	}
}

func TestCompress_NoModel(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "pro")
	history := []Message{{Role: "user", Content: "hello"}}
	result, err := c.Compress(LayerFullCompact, nil, history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected unchanged history with no model, got %d", len(result))
	}
}

// --- findSafeSplitPoint ---

func TestFindSafeSplitPoint(t *testing.T) {
	tests := []struct {
		name      string
		history   []Message
		minFresh  int
		wantIdx   int // split before this index
	}{
		{
			name: "empty",
			history: []Message{},
			minFresh: 10,
			wantIdx: 0,
		},
		{
			name: "short history, no minFresh match",
			history: []Message{
				{Role: "user", Content: "hi"},
			},
			minFresh: 10,
			wantIdx: 0,
		},
		{
			name: "user message boundary",
			history: []Message{
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "response"},
				{Role: "user", Content: "second"},
				{Role: "assistant", Content: "response2"},
				{Role: "user", Content: "third"},
			},
			minFresh: 3,
			wantIdx: 3, // split before assistant "response2" (after user "second")
		},
		{
			name: "assistant without tool calls after user",
			history: []Message{
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "reply"},
			},
			minFresh: 1,
			wantIdx: 0, // split before user; assistant follows user so returns i-1=0
		},
		{
			name: "tool call assistant not a safe split",
			history: []Message{
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "reply", ToolCalls: []MessageToolCall{{ID: "c1"}}},
			},
			minFresh: 1,
			wantIdx: 1, // can't split on assistant with tool calls, falls through to user boundary
		},
	}
	for _, tt := range tests {
		got := findSafeSplitPoint(tt.history, tt.minFresh)
		if got != tt.wantIdx {
			t.Errorf("%s: findSafeSplitPoint = %d, want %d", tt.name, got, tt.wantIdx)
		}
	}
}

// findSafeSplitPoint expects minFresh >= 0; negative values trigger production code bug (out of bounds)

// --- extractPreviousArchive ---

func TestExtractPreviousArchive(t *testing.T) {
	tests := []struct {
		name    string
		history []Message
		want    string
	}{
		{
			name: "no archive",
			history: []Message{
				{Role: "user", Content: "hello"},
			},
			want: "",
		},
		{
			name: "with archive",
			history: []Message{
				{Role: "user", Content: "hello"},
				{Role: "system", Content: "[SESSION ARCHIVE]\ntest summary"},
			},
			want: "test summary",
		},
		{
			name: "multiple archives, returns latest",
			history: []Message{
				{Role: "system", Content: "[SESSION ARCHIVE]\nolder"},
				{Role: "user", Content: "middle"},
				{Role: "system", Content: "[SESSION ARCHIVE]\nnewer"},
			},
			want: "newer",
		},
	}
	for _, tt := range tests {
		got := extractPreviousArchive(tt.history)
		if got != tt.want {
			t.Errorf("%s: extractPreviousArchive = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// --- ParseArchiveSummary ---

func TestParseArchiveSummary(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantGoal string
	}{
		{
			name:    "valid json",
			input:   `{"goal":"fix bug","decisions":["use X"],"key_findings":["found Y"],"open_issues":[]}`,
			wantOK:  true,
			wantGoal: "fix bug",
		},
		{
			name:    "wrapped in markdown",
			input:   "```json\n{\"goal\":\"refactor\"}\n```",
			wantOK:  true,
			wantGoal: "refactor",
		},
		{
			name:    "invalid json",
			input:   `not json`,
			wantOK:  false,
		},
		{
			name:    "empty object",
			input:   `{}`,
			wantOK:  true,
		},
	}
	for _, tt := range tests {
		summary, err := ParseArchiveSummary(tt.input)
		if (err == nil) != tt.wantOK {
			t.Errorf("%s: ParseArchiveSummary err = %v, wantOK=%v", tt.name, err, tt.wantOK)
			continue
		}
		if tt.wantOK && summary.Goal != tt.wantGoal {
			t.Errorf("%s: Goal = %q, want %q", tt.name, summary.Goal, tt.wantGoal)
		}
	}
}

// --- containsDecisionText ---

func TestContainsDecisionText(t *testing.T) {
	tests := []struct {
		name     string
		decisions []Decision
		text     string
		want     bool
	}{
		{"empty", nil, "foo", false},
		{"found", []Decision{{ID: "d1", Text: "use X"}}, "use X", true},
		{"not found", []Decision{{ID: "d1", Text: "use X"}}, "use Y", false},
		{"multiple, found", []Decision{{ID: "d1", Text: "a"}, {ID: "d2", Text: "b"}}, "b", true},
	}
	for _, tt := range tests {
		if got := containsDecisionText(tt.decisions, tt.text); got != tt.want {
			t.Errorf("%s: containsDecisionText = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// --- buildArchivePrompt ---

func TestBuildArchivePrompt_IncludesGoal(t *testing.T) {
	state := &TaskState{Goal: "add authentication"}
	history := []Message{{Role: "user", Content: "hello"}}
	prompt := buildArchivePrompt(state, history)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strContains(prompt, "add authentication") {
		t.Error("prompt should contain the goal")
	}
}

func TestBuildArchivePrompt_NoGoal(t *testing.T) {
	state := &TaskState{}
	history := []Message{{Role: "user", Content: "hello"}}
	prompt := buildArchivePrompt(state, history)
	// Should not contain empty goal string
	if strContains(prompt, "Task goal:") {
		t.Error("prompt should not show empty goal")
	}
}

func TestBuildArchivePrompt_SkipsSessionArchive(t *testing.T) {
	state := &TaskState{}
	history := []Message{
		{Role: "system", Content: "[SESSION ARCHIVE]\nold stuff"},
		{Role: "user", Content: "new message"},
	}
	prompt := buildArchivePrompt(state, history)
	if strContains(prompt, "[SESSION ARCHIVE]") {
		t.Error("prompt should skip SESSION ARCHIVE messages")
	}
	if !strContains(prompt, "new message") {
		t.Error("prompt should include non-archive messages")
	}
}

func TestBuildArchivePrompt_IncludesMemoryMarkers(t *testing.T) {
	state := &TaskState{MemoryMarkers: []string{"key: config.go", "note: use X"}}
	history := []Message{{Role: "user", Content: "hello"}}
	prompt := buildArchivePrompt(state, history)
	if !strContains(prompt, "key: config.go") {
		t.Error("prompt should include memory markers")
	}
	if !strContains(prompt, "note: use X") {
		t.Error("prompt should include all memory markers")
	}
}

func TestBuildArchivePrompt_IncludesDecisionsAndOpenIssues(t *testing.T) {
	state := &TaskState{
		Decisions:     []Decision{{ID: "d1", Text: "use X library"}},
		OpenQuestions: []string{"how to handle error"},
	}
	history := []Message{{Role: "user", Content: "hello"}}
	prompt := buildArchivePrompt(state, history)
	if !strContains(prompt, "use X library") {
		t.Error("prompt should include decisions")
	}
	if !strContains(prompt, "how to handle error") {
		t.Error("prompt should include open issues")
	}
}

func TestBuildArchivePrompt_NoMemoryMarkers(t *testing.T) {
	state := &TaskState{MemoryMarkers: nil}
	history := []Message{{Role: "user", Content: "hello"}}
	prompt := buildArchivePrompt(state, history)
	if strContains(prompt, "Memory markers") {
		t.Error("prompt should not mention memory markers when empty")
	}
}

// --- buildModelArchivePrompt ---

func TestBuildModelArchivePrompt(t *testing.T) {
	goal := "fix bug"
	history := []ModelMessage{{Role: "user", Content: "hello"}}
	prompt := buildModelArchivePrompt(goal, history)
	if !strContains(prompt, "fix bug") {
		t.Error("prompt should contain goal")
	}
	if !strContains(prompt, "hello") {
		t.Error("prompt should contain history")
	}
}

func TestBuildModelArchivePrompt_ExtractsPreviousArchive(t *testing.T) {
	goal := "fix"
	history := []ModelMessage{
		{Role: "system", Content: "[SESSION ARCHIVE]\nprev summary"},
		{Role: "user", Content: "new msg"},
	}
	prompt := buildModelArchivePrompt(goal, history)
	if !strContains(prompt, "prev summary") {
		t.Error("prompt should include previous archive")
	}
	if strContains(prompt, "[SESSION ARCHIVE]") {
		t.Error("prompt should skip archive messages in content")
	}
}

func TestBuildModelArchivePrompt_EmptyGoal(t *testing.T) {
	history := []ModelMessage{{Role: "user", Content: "hi"}}
	prompt := buildModelArchivePrompt("", history)
	if strContains(prompt, "Task goal:") {
		t.Error("empty goal should not be mentioned")
	}
}

// --- EstimateTokens ---

func TestEstimateTokens_NilEstimator(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "pro")
	// Estimator is nil, so EstimateToken should return 0
	msgs := []ModelMessage{{Role: "user", Content: "hello"}}
	if n := c.EstimateTokens(msgs); n != 0 {
		t.Errorf("expected 0 with nil estimator, got %d", n)
	}
}

func TestCompressModelMessages_NoOpOnToolGovernance(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "pro")
	history := []ModelMessage{{Role: "user", Content: "hello"}}
	result, err := c.CompressModelMessages(LayerToolGovernance, "goal", history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected unchanged history, got %d", len(result))
	}
}

func TestCompressModelMessages_NoModel(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "pro")
	history := []ModelMessage{{Role: "user", Content: "hello"}}
	result, err := c.CompressModelMessages(LayerFullCompact, "goal", history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected unchanged with no model, got %d", len(result))
	}
}

// helpers

func strContains(s, substr string) bool {
	return len(s) >= len(substr) && indexOfStr(s, substr) >= 0
}

func indexOfStr(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Ensure imports are used
var _ = json.RawMessage{}
var _ = time.Nanosecond
