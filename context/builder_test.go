package context

import (
	"strings"
	"testing"
	"time"

	"github.com/deepact/deepact/context/promptset"
	"github.com/deepact/deepact/engine"
)

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
		t.Error("the system prompt should carry the language-pack header")
	}
	if !strings.Contains(msgs[1].Content, "# Block S: Session Context (Stable)") {
		t.Error("the stable context should carry the English Block S header")
	}
}

// TestBuild_SkillsBlockInSystemMessage locks the injection position chosen for
// this change: the skills catalog ships inside the system message so the model
// treats it as a system-level instruction — it still decides whether to load a
// skill. It must no longer appear as a separate message.
func TestBuild_SkillsBlockInSystemMessage(t *testing.T) {
	assembler := NewContextAssembler(".", nil)
	state := &engine.TaskState{}
	assembler.SetSkillsBlock("## Available Skills\n- **x**: y")
	history := []engine.Message{{Role: "user", Content: "帮我分析这个错误"}}

	assembler.Build(state, history, nil) // warm-up build (mirrors executeTurn)
	msgs := assembler.Build(state, history, nil)

	const marker = "- **x**: y"
	if msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, marker) {
		t.Fatalf("skills catalog must live in the system message, got msgs[0].Role=%q", msgs[0].Role)
	}
	for i, m := range msgs[1:] {
		if strings.Contains(m.Content, marker) {
			t.Fatalf("skills catalog must not be injected as a separate message (msgs[%d].Role=%q)", i+1, m.Role)
		}
	}
}

// TestBuild_SystemPromptBuiltOnFirstBuild locks the eager build: the prompt is
// language-independent, so there is no placeholder window and the first Build
// already carries the canonical prompt and the Block S header.
func TestBuild_SystemPromptBuiltOnFirstBuild(t *testing.T) {
	assembler := NewContextAssembler(".", nil)
	state := &engine.TaskState{}

	msgs := assembler.Build(state, nil, nil)
	if msgs[0].Content == "" || msgs[0].Content == "(loading...)" {
		t.Fatalf("the system prompt must be built on the first Build, got %q", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "Detected project languages") {
		t.Error("the system prompt should carry the language-pack header")
	}
	if !strings.Contains(msgs[1].Content, "# Block S: Session Context (Stable)") {
		t.Error("the stable context should carry the English Block S header")
	}
}
