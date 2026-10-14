package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deepact/deepact/engine"
)

// TestBuiltinCommandsCoverDocumentedSlashCommands is the refactor's safety net:
// moving the static table into the registry must not drop or reword a command.
func TestBuiltinCommandsCoverDocumentedSlashCommands(t *testing.T) {
	want := map[string]string{ // name -> args placeholder
		"/help":   "",
		"/clear":  "",
		"/ratd":   "<需求>",
		"/collab": "<需求>",
		"/review": "<范围>",
		"/resume": "",
	}
	builtins := builtinCommands()
	got := make(map[string]string, len(builtins))
	for _, cmd := range builtins {
		got[cmd.Name] = cmd.Args
	}
	for name, args := range want {
		args2, ok := got[name]
		if !ok {
			t.Errorf("registry is missing %s", name)
			continue
		}
		if args2 != args {
			t.Errorf("%s args placeholder = %q, want %q", name, args2, args)
		}
	}
	if len(got) != len(want) {
		t.Errorf("registry has %d commands, want %d: %v", len(got), len(want), got)
	}
}

func TestLookupCommand(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})

	tests := []struct {
		name     string
		input    string
		wantName string
		wantArgs string
		wantOK   bool
	}{
		{name: "exact match", input: "/help", wantName: "/help", wantOK: true},
		{name: "case insensitive", input: "/HELP", wantName: "/help", wantOK: true},
		{name: "surrounding whitespace ignored", input: "  /resume  ", wantName: "/resume", wantOK: true},
		{
			name:  "no-arg command with arguments falls through",
			input: "/help me", wantOK: false,
		},
		{name: "unknown command", input: "/nope", wantOK: false},
		{name: "plain text", input: "fix the bug", wantOK: false},
		{name: "empty", input: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, args, ok := lookupCommand(m.allCommands(), tt.input)
			if ok != tt.wantOK {
				t.Fatalf("lookupCommand(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if cmd.Name != tt.wantName {
				t.Errorf("name = %q, want %q", cmd.Name, tt.wantName)
			}
			if args != tt.wantArgs {
				t.Errorf("args = %q, want %q", args, tt.wantArgs)
			}
		})
	}
}

// TestLocalCommandFor_Availability is the run-time gate: a command that is not
// marked available during a run must fall through to the steer channel.
func TestLocalCommandFor_Availability(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})

	tests := []struct {
		name    string
		input   string
		running bool
		wantOK  bool
	}{
		{name: "help while idle", input: "/help", running: false, wantOK: true},
		{name: "help during a run", input: "/help", running: true, wantOK: true},
		{name: "resume while idle", input: "/resume", running: false, wantOK: true},
		{name: "resume during a run falls through", input: "/resume", running: true, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, ok := m.localCommandFor(tt.input, tt.running)
			if ok != tt.wantOK {
				t.Errorf("localCommandFor(%q, running=%v) ok = %v, want %v", tt.input, tt.running, ok, tt.wantOK)
			}
		})
	}
}

// TestSkillEntriesAreRegisteredButEngineOwned: skills are dynamic registry
// entries (no more special-cased branch in the suggestion popup), but the UI
// must never intercept them — the engine handles /<skill> itself.
func TestSkillEntriesAreRegisteredButEngineOwned(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.skillSuggestions = []Suggestion{{Command: "brainstorming", Description: "explore intent"}}

	var found bool
	for _, cmd := range m.allCommands() {
		if cmd.Name != "/brainstorming" {
			continue
		}
		found = true
		if cmd.Source != commandSourceSkill {
			t.Errorf("skill entry source = %v, want skill", cmd.Source)
		}
		if cmd.Handle != nil {
			t.Error("skill entries must stay engine-owned (nil Handle)")
		}
	}
	if !found {
		t.Fatal("skill entry not registered: /brainstorming missing from allCommands()")
	}
	if _, _, ok := m.localCommandFor("/brainstorming", false); ok {
		t.Error("skill command must not be handled locally")
	}
}

// TestSuggestionPopupIncludesSkills: the popup consumes the registry, so a
// skill typed as a prefix shows up exactly once.
func TestSuggestionPopupIncludesSkills(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.skillSuggestions = []Suggestion{{Command: "brainstorming", Description: "explore intent"}}

	m.inputBuf.SetValue("/brain")
	m.updateSuggestions()

	if len(m.suggestions) != 1 {
		t.Fatalf("suggestions = %+v, want exactly one entry", m.suggestions)
	}
	if m.suggestions[0].Command != "/brainstorming" {
		t.Errorf("suggestion = %q, want /brainstorming", m.suggestions[0].Command)
	}
}

func TestHandleHelpCommand(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})

	got, _, handled := handleHelpCommand(&m, "")
	if !handled {
		t.Fatal("handleHelpCommand should report handled")
	}
	if got.state == stateRunning {
		t.Error("/help must not put the model into stateRunning")
	}
	if len(got.messages) == 0 {
		t.Fatal("no message appended")
	}
	last := got.messages[len(got.messages)-1]
	for _, want := range []string{"/help", "/ratd", "/resume", "Available Tools"} {
		if !strings.Contains(last.Content, want) {
			t.Errorf("help text missing %q", want)
		}
	}
}

func TestHandleResumeCommand_NoEngine(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})

	got, _, handled := handleResumeCommand(&m, "")
	if !handled {
		t.Fatal("handleResumeCommand should report handled")
	}
	if len(got.messages) != 1 || !strings.Contains(got.messages[0].Content, "API key required") {
		t.Fatalf("messages = %+v, want the API-key hint", got.messages)
	}
}

func TestHandleResumeCommand_EntersPicker(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.engine = &mockResumeRunner{sessions: []SessionSummary{{ID: "s1"}}}

	got, _, handled := handleResumeCommand(&m, "")
	if !handled {
		t.Fatal("handleResumeCommand should report handled")
	}
	if got.state != stateResume {
		t.Errorf("state = %v, want stateResume", got.state)
	}
	if len(got.resumeSessions) != 1 {
		t.Errorf("resumeSessions = %+v, want one entry", got.resumeSessions)
	}
}

// TestSubmitInput_HelpRunsLocally covers the dispatch path end to end.
func TestSubmitInput_HelpRunsLocally(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.inputBuf.SetValue("/help")

	result, _ := m.submitInput()
	m2 := result.(Model)

	if m2.state == stateRunning {
		t.Error("/help must not start a run")
	}
	if m2.inputBuf.Value() != "" {
		t.Errorf("input not cleared: %q", m2.inputBuf.Value())
	}
	if len(m2.messages) < 2 {
		t.Fatalf("messages = %+v, want the user line plus help", m2.messages)
	}
	if last := m2.messages[len(m2.messages)-1]; !strings.Contains(last.Content, "Slash Commands") {
		t.Errorf("last message is not the help screen: %q", last.Content)
	}
}

// TestSubmitInput_EngineOwnedCommandFallsThrough: /clear is in the registry for
// display only. With no engine it must reach the engine path (and report the
// missing API key) rather than being silently swallowed by the UI.
func TestSubmitInput_EngineOwnedCommandFallsThrough(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.inputBuf.SetValue("/clear")

	result, _ := m.submitInput()
	m2 := result.(Model)

	if len(m2.messages) != 2 {
		t.Fatalf("messages = %+v, want user line + engine-path hint", m2.messages)
	}
	if !strings.Contains(m2.messages[1].Content, "API key required") {
		t.Errorf("engine-owned command did not fall through: %q", m2.messages[1].Content)
	}
}

// TestRunningStateSlashHelpAnsweredLocally is the one intentional behaviour
// change of this refactor: /help during a run is answered by the UI instead of
// being queued to the model as a steer message.
func TestRunningStateSlashHelpAnsweredLocally(t *testing.T) {
	r := &recordingRunner{}
	m := NewModel(nil, engine.PricingConfig{})
	m.engine = r
	m.state = stateRunning
	m.height = 40
	m.width = 80
	m.inputBuf.SetValue("/help")

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := result.(Model)

	if len(r.steered) != 0 {
		t.Fatalf("Steer calls = %v, want none for /help", r.steered)
	}
	if m2.state != stateRunning {
		t.Errorf("state = %v, want it to stay running", m2.state)
	}
	last := m2.messages[len(m2.messages)-1]
	if !strings.Contains(last.Content, "Slash Commands") {
		t.Errorf("last message is not the help screen: %q", last.Content)
	}
}

// TestRunningStateSlashResumeStillSteers: /resume cannot be answered while a run
// is in flight (it would swap the history underneath it), so it keeps the
// historical steer behaviour.
func TestRunningStateSlashResumeStillSteers(t *testing.T) {
	r := &recordingRunner{}
	m := NewModel(nil, engine.PricingConfig{})
	m.engine = r
	m.state = stateRunning
	m.height = 40
	m.width = 80
	m.inputBuf.SetValue("/resume")

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := result.(Model)

	if len(r.steered) != 1 || r.steered[0] != "/resume" {
		t.Fatalf("Steer calls = %v, want [/resume]", r.steered)
	}
	if m2.state != stateRunning {
		t.Errorf("state = %v, want it to stay running", m2.state)
	}
}
