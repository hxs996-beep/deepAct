package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// CommandSource tells /help which section a command belongs to. Built-in
// commands and skill shortcuts are one registry but two sections on screen.
type CommandSource int

const (
	commandSourceBuiltin CommandSource = iota
	commandSourceSkill
)

// Command is one entry of the UI command registry. The registry is the single
// source of truth for three consumers that used to keep separate lists: the
// /-suggestion popup, the /help screen, and local dispatch in submitInput.
//
// A Command with a nil Handle is declared for display only — the engine owns
// it. /clear, /ratd, /collab and every /<skill> shortcut are of that kind: the
// UI must never intercept them (engine/loop.go handles /clear and
// parseSkillCommand), it only advertises them.
type Command struct {
	Name        string // includes the leading slash, e.g. "/help"
	Args        string // argument placeholder shown in the menu; "" = takes no argument
	Description string
	Source      CommandSource
	// AvailableDuringRun: run the command locally even while a Run is in
	// flight. When false, submitting it during a run goes to the steer channel
	// (the behaviour every command had before this registry existed).
	AvailableDuringRun bool
	// Handle runs the command locally. handled=false falls through to the
	// default path: engine.Run when idle, steer when running.
	Handle func(m *Model, args string) (Model, tea.Cmd, bool)
}

// builtinCommands is the static part of the registry. It is a function rather
// than a package variable because the handlers read the registry back through
// Model.allCommands() — a var would be an initialization cycle.
func builtinCommands() []Command {
	return []Command{
		{
			Name:               "/help",
			Description:        "Show this help screen",
			AvailableDuringRun: true,
			Handle:             handleHelpCommand,
		},
		{Name: "/clear", Description: "Reset session state (clear messages and context)"},
		{Name: "/ratd", Args: "<需求>", Description: "反向测试驱动：内置技能（红队写对抗测试驱动实现）"},
		{Name: "/collab", Args: "<需求>", Description: "并行研究：内置技能（拆解→并行委派调研→汇总）"},
		{Name: "/review", Args: "<范围>", Description: "独立复核：内置技能（对抗式审查指定范围的改动，输出带优先级与证据的发现）"},
		{Name: "/resume", Description: "恢复之前的会话", Handle: handleResumeCommand},
	}
}

// allCommands is the registry a model actually sees: built-ins plus the skill
// shortcuts registered at startup. Skills are ordinary entries here, not a
// special case bolted onto the suggestion popup.
func (m Model) allCommands() []Command {
	builtins := builtinCommands()
	commands := make([]Command, 0, len(builtins)+len(m.skillSuggestions))
	commands = append(commands, builtins...)
	for _, skill := range m.skillSuggestions {
		commands = append(commands, Command{
			Name:        "/" + skill.Command,
			Args:        skill.Args,
			Description: skill.Description,
			Source:      commandSourceSkill,
		})
	}
	return commands
}

// lookupCommand matches a submitted line against the registry. Matching is on
// the first token and case-insensitive. A command that declares no argument
// placeholder only matches when nothing follows it, so "/help me" keeps falling
// through to the engine exactly as it did before.
func lookupCommand(commands []Command, input string) (Command, string, bool) {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return Command{}, "", false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return Command{}, "", false
	}
	name := strings.ToLower(fields[0])
	args := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))

	for _, cmd := range commands {
		if strings.ToLower(cmd.Name) != name {
			continue
		}
		if cmd.Args == "" && args != "" {
			return Command{}, "", false
		}
		return cmd, args, true
	}
	return Command{}, "", false
}

// localCommandFor returns the command that should run locally for this input.
// ok is false when the input is not a registered command, is engine-owned
// (nil Handle), or is not available in the current state — in every one of
// those cases the caller falls through to the existing engine/steer path.
func (m Model) localCommandFor(input string, running bool) (Command, string, bool) {
	cmd, args, ok := lookupCommand(m.allCommands(), input)
	if !ok || cmd.Handle == nil {
		return Command{}, "", false
	}
	if running && !cmd.AvailableDuringRun {
		return Command{}, "", false
	}
	return cmd, args, true
}

// handleHelpCommand renders the help screen from the registry.
func handleHelpCommand(m *Model, _ string) (Model, tea.Cmd, bool) {
	// Keep an in-flight run's state intact: /help is available during a run, and
	// resetting to stateReady there would make the UI believe the run had ended
	// (stuck spinner, Esc no longer cancels, next Enter starts a second run).
	if m.state != stateRunning {
		m.state = stateReady
	}
	m.messages = append(m.messages, DisplayMessage{
		Role:    "assistant",
		Content: buildHelpText(m.allCommands(), helpTools),
	})
	return *m, nil, true
}

// handleResumeCommand opens the session picker.
func handleResumeCommand(m *Model, _ string) (Model, tea.Cmd, bool) {
	if m.engine == nil {
		m.messages = append(m.messages, DisplayMessage{Role: "system", Content: "API key required. Restart and enter key."})
		return *m, nil, true
	}
	sessions := m.engine.ListSessions()
	if len(sessions) == 0 {
		m.messages = append(m.messages, DisplayMessage{Role: "system", Content: "没有可恢复的会话。"})
		return *m, nil, true
	}
	m.resumeSessions = sessions
	m.selectedResume = 0
	m.state = stateResume
	return *m, nil, true
}
