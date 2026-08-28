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
		{"max is 0", "hello", 0, "..."},
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

func TestFormatTaskStateVolatile_EngineOnlyFields(t *testing.T) {
	// read_history / full modified_files / full decisions are engine-layer state,
	// NOT rendered to the prompt. The rendered Block B exposes a count + recent
	// window for the model's current reasoning only.
	state := &engine.TaskState{
		Goal:          "fix the race",
		TurnNumber:    7,
		ModifiedFiles: []string{"a.go", "b.go", "c.go", "d.go"},
		ReadHistory:   []engine.ReadRecord{{Path: "x.go", Scope: ""}, {Path: "y.go", Scope: "symbol:Run"}},
		Decisions:     []engine.Decision{{ID: "d-1", Text: "use sync.Mutex"}, {ID: "d-2", Text: "add test"}},
	}
	got := formatTaskStateVolatile(state)

	// Absent from the rendered JSON (engine-only).
	for _, banned := range []string{`"read_history"`, `"modified_files"`} {
		if strContains(got, banned) {
			t.Errorf("rendered Block B must not contain %s:\n%s", banned, got)
		}
	}
	// Present: the count + recent window instead of the full list.
	if !strContains(got, `"modified_count":4`) {
		t.Errorf("expected modified_count=4:\n%s", got)
	}
	if !strContains(got, `"recent_modified"`) {
		t.Errorf("expected recent_modified window:\n%s", got)
	}
	// decisions are rendered as the recent window.
	if strContains(got, `"decisions"`) {
		t.Errorf("full decisions field should be replaced by recent_decisions:\n%s", got)
	}
	if !strContains(got, `"recent_decisions"`) {
		t.Errorf("expected recent_decisions window:\n%s", got)
	}
}

func TestFormatTaskStateVolatile_CapsWindows(t *testing.T) {
	// The recent window is capped so a long session does not grow the tail.
	var modified []string
	for i := 0; i < 60; i++ {
		modified = append(modified, "f"+itoaForTest(i)+".go")
	}
	state := &engine.TaskState{ModifiedFiles: modified}
	got := formatTaskStateVolatile(state)
	if !strContains(got, `"modified_count":60`) {
		t.Errorf("expected modified_count=60:\n%s", got)
	}
	if strContains(got, "f0.go") {
		t.Errorf("recent window should drop the oldest file (f0.go):\n%s", got)
	}
	if !strContains(got, "f59.go") {
		t.Errorf("recent window should keep the newest file (f59.go):\n%s", got)
	}
}

func TestLastN(t *testing.T) {
	in := []string{"a", "b", "c"}
	if got := lastN(in, 3); len(got) != 3 || got[0] != "a" {
		t.Errorf("lastN(in,3) = %v", got)
	}
	if got := lastN(in, 2); len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Errorf("lastN(in,2) = %v", got)
	}
	if got := lastN(in, 0); len(got) != 0 {
		t.Errorf("lastN(in,0) = %v, want empty", got)
	}
	if got := lastN(nil, 5); got != nil {
		t.Errorf("lastN(nil,5) = %v, want nil", got)
	}
}

func itoaForTest(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestBuild_AgentsBlockInStableZone(t *testing.T) {
	assembler := NewContextAssembler(".", nil)
	assembler.userLang = "中文"
	assembler.userLangSet = true

	agentsContent := "\n## Project Conventions (AGENTS.md)\n\n### AGENTS.md\n\n项目规则：用标准库\n"
	assembler.SetAgentsBlock(agentsContent)

	state := &engine.TaskState{}
	msgs := assembler.Build(state, nil, nil)
	found := false
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "项目规则：用标准库") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Build() should include AGENTS.md content in stable zone")
	}
}

func TestBuild_ActiveSkillInTail(t *testing.T) {
	assembler := NewContextAssembler(".", nil)
	assembler.userLang = "中文"
	assembler.userLangSet = true
	assembler.stableSessionBlock = "stable"
	assembler.skillsBlock = "[skills]"

	assembler.SetActiveSkill("writing-plans", "1. 先写计划\n2. 再执行")

	state := &engine.TaskState{Goal: "g"}
	history := []engine.Message{
		{Role: "user", Content: "开始"},
		{Role: "assistant", Content: "好的"},
	}
	msgs := assembler.Build(state, history, nil)

	lastHistoryIdx := -1
	skillIdx := -1
	for i, msg := range msgs {
		if msg.Content == "开始" || msg.Content == "好的" {
			lastHistoryIdx = i
		}
		if strings.Contains(msg.Content, "[SKILL ACTIVATED: writing-plans]") {
			skillIdx = i
		}
	}
	if skillIdx == -1 {
		t.Fatalf("active skill message not found in Build output")
	}
	if lastHistoryIdx == -1 {
		t.Fatalf("history messages not found in Build output")
	}
	// The skill methodology must sit AFTER history (tail snapshot, recency), not
	// in the stable zone before it — so a skill change only touches the tail and
	// never shifts the cached history prefix.
	if skillIdx <= lastHistoryIdx {
		t.Errorf("active skill message should come AFTER history; skillIdx=%d lastHistoryIdx=%d", skillIdx, lastHistoryIdx)
	}
	// It must appear before Block B (the very last "current state" message).
	for j := skillIdx + 1; j < len(msgs); j++ {
		if strings.Contains(msgs[j].Content, "Block B") {
			if !strings.Contains(msgs[skillIdx].Content, "1. 先写计划") {
				t.Errorf("skill methodology text should be preserved in tail block")
			}
			return
		}
	}
	t.Errorf("Block B not found after the active skill message")
}

func TestBuildBlockB_SupersedesPhrase(t *testing.T) {
	zh := BuildBlockB(`{"goal":"g"}`, "中文")
	for _, want := range []string{"Block B", "权威", "覆盖此前", "实时状态"} {
		if !strContains(zh, want) {
			t.Errorf("zh Block B missing %q:\n%s", want, zh)
		}
	}
	en := BuildBlockB(`{"goal":"g"}`, "en")
	for _, want := range []string{"Block B", "authoritative", "supersedes", "Live State"} {
		if !strContains(en, want) {
			t.Errorf("en Block B missing %q:\n%s", want, en)
		}
	}
}
