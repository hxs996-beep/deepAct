package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deepact/deepact/engine"
)

// TestSuggestionTabDismiss_ForcesRepaint locks the "input box top background
// doesn't recover after selecting a skill" bug: dismissing the suggestion
// popup with Tab returns m, nil (no repaint), so the popup's background
// pixels persist on the terminal until a resize (WindowSizeMsg) forces a full
// repaint. Every other visual transition in the codebase (scroll, mouse,
// content_delta, engine response) calls repaintCmd; the popup dismiss paths
// are the exception. Tab must return a repaint Cmd.
func TestSuggestionTabDismiss_ForcesRepaint(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.ready = true
	m.state = stateReady
	m.width = 100
	m.height = 30
	m.messages = []DisplayMessage{{Role: "user", Content: "分析这个问题"}}
	m.suggestions = []Suggestion{
		{Command: "/collab", Description: "multi-role collaboration pipeline"},
		{Command: "/clear", Description: "reset session"},
	}
	m.showSuggestions = true
	m.selectedSuggestion = 0

	got, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if got.(Model).showSuggestions {
		t.Fatal("Tab must dismiss the suggestion popup")
	}
	if cmd == nil {
		t.Fatal("dismissing the suggestion popup must return a repaint Cmd; otherwise stale popup background pixels persist until a terminal resize")
	}
}

// TestSuggestionEnterDismiss_ForcesRepaint: same contract for the plain-Enter
// autocomplete path (no Alt), which also dismisses the popup and returns nil.
func TestSuggestionEnterDismiss_ForcesRepaint(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.ready = true
	m.state = stateReady
	m.width = 100
	m.height = 30
	m.messages = []DisplayMessage{{Role: "user", Content: "分析这个问题"}}
	m.suggestions = []Suggestion{
		{Command: "/collab", Description: "multi-role collaboration pipeline"},
	}
	m.showSuggestions = true
	m.selectedSuggestion = 0

	got, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if got.(Model).showSuggestions {
		t.Fatal("Enter must dismiss the suggestion popup")
	}
	if cmd == nil {
		t.Fatal("dismissing the suggestion popup via Enter must return a repaint Cmd; otherwise stale popup background pixels persist until a terminal resize")
	}
}

// TestOptionsTabDismiss_ForcesRepaint: the options popup's Tab path types the
// selected option's number into the input and dismisses the popup — it must
// also force a repaint so the popup background is not left on the terminal.
func TestOptionsTabDismiss_ForcesRepaint(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.ready = true
	m.state = stateReady
	m.width = 100
	m.height = 30
	m.messages = []DisplayMessage{{Role: "user", Content: "分析这个问题"}}
	m.activeOptions = []string{
		"按报告执行",
		"输入你的意见",
	}
	m.selectedOption = 0

	got, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if len(got.(Model).activeOptions) != 0 {
		t.Fatal("Tab must dismiss the options popup")
	}
	if cmd == nil {
		t.Fatal("dismissing the options popup via Tab must return a repaint Cmd")
	}
}

// TestOptionsEnterLastItemDismiss_ForcesRepaint: selecting the trailing
// "输入你的意见" item closes the popup and returns to the input box — it must
// also force a repaint so the popup background is not left on the terminal.
func TestOptionsEnterLastItemDismiss_ForcesRepaint(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.ready = true
	m.state = stateReady
	m.width = 100
	m.height = 30
	m.messages = []DisplayMessage{{Role: "user", Content: "分析这个问题"}}
	m.activeOptions = []string{
		"按报告执行",
		"输入你的意见",
	}
	m.selectedOption = 1 // last item -> free input

	got, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if len(got.(Model).activeOptions) != 0 {
		t.Fatal("Enter on the last option must dismiss the options popup")
	}
	if cmd == nil {
		t.Fatal("dismissing the options popup via Enter on the last item must return a repaint Cmd")
	}
}
