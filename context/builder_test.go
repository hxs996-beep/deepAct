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
