package ui

import (
	"strings"
	"testing"
)

func TestBuildHelpText_IncludesSections(t *testing.T) {
	commands := []Command{
		{Name: "/ratd", Args: "<需求>", Description: "reverse-test-driven harness"},
		{Name: "/brainstorming", Description: "explore intent", Source: commandSourceSkill},
	}
	tools := []Suggestion{
		{Command: "read", Description: "read a file"},
		{Command: "web_search", Description: "search the web"},
		{Command: "handoff_to_agent", Description: "delegate"},
	}
	out := buildHelpText(commands, tools)

	for _, header := range []string{"# DeepAct", "## Keyboard Shortcuts", "## Slash Commands", "## Skills", "## Available Tools"} {
		if !strings.Contains(out, header) {
			t.Errorf("help text missing section %q:\n%s", header, out)
		}
	}
}

func TestBuildHelpText_ListsCommandsSkillsAndTools(t *testing.T) {
	commands := []Command{
		{Name: "/ratd", Args: "<需求>", Description: "x"},
		{Name: "/clear", Description: "y"},
		{Name: "/brainstorming", Description: "explore intent", Source: commandSourceSkill},
	}
	tools := []Suggestion{
		{Command: "read", Description: "read"},
		{Command: "web_search", Description: "search"},
		{Command: "handoff_to_agent", Description: "delegate"},
	}
	out := buildHelpText(commands, tools)

	for _, want := range []string{"/ratd", "/clear", "/brainstorming", "web_search", "handoff_to_agent", "read"} {
		if !strings.Contains(out, want) {
			t.Errorf("help text missing %q:\n%s", want, out)
		}
	}
	// Skills must be rendered as /shortcuts (command is bare name).
	if !strings.Contains(out, "/brainstorming —") {
		t.Errorf("skill should render as '/brainstorming — ...':\n%s", out)
	}
}

func TestBuildHelpText_HandlesEmptyInputs(t *testing.T) {
	out := buildHelpText(nil, nil)
	if !strings.Contains(out, "## Slash Commands") || !strings.Contains(out, "(none)") {
		t.Errorf("empty commands should render '(none)':\n%s", out)
	}
	if strings.Contains(out, "## Skills") {
		t.Errorf("empty skills should omit the Skills section:\n%s", out)
	}
}
