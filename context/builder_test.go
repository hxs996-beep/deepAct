package context

import (
	"strings"
	"testing"
	"time"

	"github.com/deepact/deepact/engine"
)

func TestHasFirstUserMessage(t *testing.T) {
	tests := []struct {
		name    string
		history []engine.Message
		want    bool
	}{
		{
			name:    "empty history",
			history: []engine.Message{},
			want:    false,
		},
		{
			name: "user message present",
			history: []engine.Message{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "hello"},
			},
			want: true,
		},
		{
			name: "only non-user messages",
			history: []engine.Message{
				{Role: "system", Content: "system prompt"},
				{Role: "assistant", Content: "response"},
			},
			want: false,
		},
		{
			name: "user message with only whitespace",
			history: []engine.Message{
				{Role: "user", Content: "   "},
			},
			want: false,
		},
		{
			name: "multiple messages with user",
			history: []engine.Message{
				{Role: "assistant", Content: "first"},
				{Role: "user", Content: "fix the bug"},
				{Role: "assistant", Content: "ok"},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		if got := hasFirstUserMessage(tt.history); got != tt.want {
			t.Errorf("%s: hasFirstUserMessage = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestTruncString(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"shorter than max", "hello", 10, "hello"},
		{"equal to max", "hello", 5, "hello"},
		{"longer than max", "hello world", 5, "hello..."},
		{"empty string", "", 5, ""},
		{"max is 0", "hello", 0, "...",},
	}
	for _, tt := range tests {
		if got := truncString(tt.s, tt.max); got != tt.want {
			t.Errorf("%s: truncString(%q, %d) = %q, want %q", tt.name, tt.s, tt.max, got, tt.want)
		}
	}
}

func TestMapMessage(t *testing.T) {
	tests := []struct {
		name string
		msg  engine.Message
	}{
		{
			name: "simple user message",
			msg:  engine.Message{Role: "user", Content: "hello", Timestamp: time.Now()},
		},
		{
			name: "assistant with tool calls",
			msg: engine.Message{
				Role:    "assistant",
				Content: "Let me check",
				ToolCalls: []engine.MessageToolCall{
					{ID: "call-1", Name: "grep", Arguments: `{"pattern":"foo"}`},
				},
				Timestamp: time.Now(),
			},
		},
		{
			name: "empty content",
			msg:  engine.Message{Role: "user", Content: "", Timestamp: time.Now()},
		},
		{
			name: "with reasoning content",
			msg:  engine.Message{Role: "assistant", Content: "answer", ReasoningContent: "thinking...", Timestamp: time.Now()},
		},
	}
	for _, tt := range tests {
		got := mapMessage(tt.msg)
		if got.Role != tt.msg.Role {
			t.Errorf("%s: Role = %q, want %q", tt.name, got.Role, tt.msg.Role)
		}
		if got.Content != tt.msg.Content {
			t.Errorf("%s: Content = %q, want %q", tt.name, got.Content, tt.msg.Content)
		}
		if got.ReasoningContent != tt.msg.ReasoningContent {
			t.Errorf("%s: ReasoningContent = %q, want %q", tt.name, got.ReasoningContent, tt.msg.ReasoningContent)
		}
		if len(tt.msg.ToolCalls) > 0 {
			if len(got.ToolCalls) != len(tt.msg.ToolCalls) {
				t.Errorf("%s: len(ToolCalls) = %d, want %d", tt.name, len(got.ToolCalls), len(tt.msg.ToolCalls))
			}
			for i, call := range tt.msg.ToolCalls {
				if got.ToolCalls[i].ID != call.ID {
					t.Errorf("%s: ToolCalls[%d].ID = %q, want %q", tt.name, i, got.ToolCalls[i].ID, call.ID)
				}
				if got.ToolCalls[i].Function.Name != call.Name {
					t.Errorf("%s: ToolCalls[%d].Name = %q, want %q", tt.name, i, got.ToolCalls[i].Function.Name, call.Name)
				}
			}
		} else if len(got.ToolCalls) > 0 {
			t.Errorf("%s: expected no ToolCalls, got %d", tt.name, len(got.ToolCalls))
		}
	}
}

func TestFormatTaskStateVolatile(t *testing.T) {
	tests := []struct {
		name  string
		state *engine.TaskState
		want  string // empty string means expect empty
	}{
		{
			name:  "nil state",
			state: nil,
			want:  "",
		},
		{
			name: "with turn number",
			state: &engine.TaskState{
				TurnNumber:          5,
				ConsecutiveFailures: 0,
				EditScopeFiles:      2,
			},
			want: `"turn_number":5`,
		},
		{
			name: "with active skill",
			state: &engine.TaskState{
				ActiveSkillName: "test-driven-development",
				TurnNumber:      1,
			},
			want: `"active_skill_name":"test-driven-development"`,
		},
		{
			name: "with consecutive failures",
			state: &engine.TaskState{
				ConsecutiveFailures: 3,
			},
			want: `"consecutive_failures":3`,
		},
	}
	for _, tt := range tests {
		got := formatTaskStateVolatile(tt.state)
		if tt.want == "" {
			if got != "" {
				t.Errorf("%s: expected empty, got %q", tt.name, got)
			}
		} else if !strContains(got, tt.want) {
			t.Errorf("%s: output should contain %q, got %q", tt.name, tt.want, got)
		}
	}
}

func TestFlattenRoundtable(t *testing.T) {
	tests := []struct {
		name string
		rt   *engine.RoundtableState
		want string // must contain this string, or empty if expect nil
	}{
		{
			name: "nil roundtable",
			rt:   nil,
			want: "",
		},
	}
	for _, tt := range tests {
		result := flattenRoundtable(tt.rt)
		if tt.want == "" {
			if result != nil {
				t.Errorf("%s: expected nil, got %+v", tt.name, result)
			}
		}
	}
}

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

func TestBuild_AnalysisModeConstraint(t *testing.T) {
	assembler := NewContextAssembler(".", nil)
	assembler.userLang = "中文"
	assembler.userLangSet = true
	assembler.stableSessionBlock = "stable"

	// AnalysisMode=true: constraint should be present
	state := &engine.TaskState{
		Goal:         "test goal",
		AnalysisMode: true,
	}
	msgs := assembler.Build(state, nil, nil)
	found := false
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "[ANALYSIS MODE]") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Build with AnalysisMode=true should include [ANALYSIS MODE] constraint")
	}

	// AnalysisMode=false: constraint should NOT be present
	state.AnalysisMode = false
	msgs = assembler.Build(state, nil, nil)
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "[ANALYSIS MODE]") {
			t.Errorf("Build with AnalysisMode=false should NOT include [ANALYSIS MODE] constraint")
			break
		}
	}
}
