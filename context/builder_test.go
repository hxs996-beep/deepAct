package context

import (
	"strings"
	"testing"
	"time"

	"github.com/deepact/deepact/context/promptset"
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

// TestBuild_EnglishSessionGetsSystemPrompt is the regression test for the
// `userLang != ""` gate: English was encoded as the empty string, so every
// non-Chinese session kept the "(loading...)" placeholder as its system prompt.
//
// Build is called twice because executeTurn always builds once for the
// compression token estimate before building the messages it actually sends;
// the second build is the one the model receives.
func TestBuild_EnglishSessionGetsSystemPrompt(t *testing.T) {
	assembler := NewContextAssembler(".", nil)
	state := &engine.TaskState{}
	history := []engine.Message{{Role: "user", Content: "fix the flaky test in engine/loop_test.go"}}

	assembler.Build(state, history, nil)
	msgs := assembler.Build(state, history, nil)
	if len(msgs) < 2 {
		t.Fatalf("Build() returned %d messages, want at least 2", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Fatalf("messages[0].Role = %q, want %q", msgs[0].Role, "system")
	}
	if msgs[0].Content == "" || msgs[0].Content == "(loading...)" {
		t.Fatalf("English session must get the real system prompt, got %q", msgs[0].Content)
	}
	if !strings.HasPrefix(msgs[0].Content, promptset.Get().System) {
		t.Error("system message does not start with the canonical prompt set")
	}
	if !strings.Contains(msgs[0].Content, "Detected project languages") {
		t.Error("English session should get the English language-pack header")
	}
	if !strings.Contains(msgs[1].Content, "# Block S: Session Context (Stable)") {
		t.Error("English session should get the English Block S header")
	}
}

// TestBuild_SystemPromptDeferredUntilFirstUserMessage keeps the placeholder's
// intended meaning: it is only for the window before the first user message,
// after which the prompt must be built from the locked language.
func TestBuild_SystemPromptDeferredUntilFirstUserMessage(t *testing.T) {
	assembler := NewContextAssembler(".", nil)
	state := &engine.TaskState{}

	if msgs := assembler.Build(state, nil, nil); msgs[0].Content != "(loading...)" {
		t.Fatalf("before the first user message system content = %q, want %q", msgs[0].Content, "(loading...)")
	}

	history := []engine.Message{{Role: "user", Content: "修复这个 bug"}}
	assembler.Build(state, history, nil) // warm-up build, mirrors executeTurn's estimation build
	msgs := assembler.Build(state, history, nil)
	if msgs[0].Content == "(loading...)" {
		t.Fatal("system prompt must be built once the first user message arrives")
	}
	if !strings.Contains(msgs[0].Content, "检测到的项目语言") {
		t.Error("Chinese session should get the Chinese language-pack header")
	}
	if !strings.Contains(msgs[1].Content, "# Block S：会话上下文（固定）") {
		t.Error("Chinese session should get the Chinese Block S header")
	}
}
