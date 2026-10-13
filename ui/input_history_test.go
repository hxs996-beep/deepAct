package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deepact/deepact/engine"
)

func TestUpRecallsLastSubmittedInput(t *testing.T) {
	m := NewModel(&recordingRunner{}, engine.PricingConfig{})
	m.state = stateReady
	m.width, m.height = 80, 40
	m.inputBuf.SetValue("帮我改下输入框")

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := result.(Model)
	if got := m2.inputBuf.Value(); got != "" {
		t.Fatalf("buffer should be cleared after submit, got %q", got)
	}

	result, _ = m2.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	m3 := result.(Model)
	if got := m3.inputBuf.Value(); got != "帮我改下输入框" {
		t.Fatalf("↑ should recall the last submission, got %q", got)
	}
}

func TestUpWithNoHistoryLeavesInputEmpty(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.width, m.height = 80, 40

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if got := result.(Model).inputBuf.Value(); got != "" {
		t.Fatalf("↑ with no history must not insert anything, got %q", got)
	}
}

func TestUpDoesNotOverwriteDraft(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.width, m.height = 80, 40
	m.lastSubmittedInput = "之前提交的内容"
	m.inputBuf.SetValue("正在写的草稿")

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if got := result.(Model).inputBuf.Value(); got != "正在写的草稿" {
		t.Fatalf("↑ must leave a non-empty draft untouched, got %q", got)
	}
}

func TestBlankSubmitIsNotRemembered(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.width, m.height = 80, 40
	m.inputBuf.SetValue("   ")

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := result.(Model)
	if m2.lastSubmittedInput != "" {
		t.Fatalf("blank submit must not become history, got %q", m2.lastSubmittedInput)
	}

	// The whitespace-only draft is still in the box (submit was a no-op) and
	// ↑ must not recall anything on top of it.
	result, _ = m2.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if got := result.(Model).inputBuf.Value(); got != "   " {
		t.Fatalf("↑ must leave the untouched draft as-is, got %q", got)
	}
}

func TestUpRecallsMessageSteeredDuringRun(t *testing.T) {
	r := &recordingRunner{}
	m := NewModel(nil, engine.PricingConfig{})
	m.engine = r
	m.state = stateRunning
	m.width, m.height = 80, 40
	m.inputBuf.SetValue("补充：也检查测试文件")

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := result.(Model)
	if len(r.steered) != 1 {
		t.Fatalf("Steer calls = %v, want one", r.steered)
	}

	result, _ = m2.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if got := result.(Model).inputBuf.Value(); got != "补充：也检查测试文件" {
		t.Fatalf("↑ during a run should recall the steered message, got %q", got)
	}
}

// TestSteeredPasteSendsAndRecallsRealContent pins the paste-shorthand case on
// the steer path: the input box shows "[Pasted +N lines]", so reading the box
// would steer (and later recall) the indicator instead of the pasted text.
func TestSteeredPasteSendsAndRecallsRealContent(t *testing.T) {
	r := &recordingRunner{}
	m := NewModel(nil, engine.PricingConfig{})
	m.engine = r
	m.state = stateRunning
	m.width, m.height = 80, 40

	pasted := "line1\nline2\nline3\nline4"
	feedWindowsPaste(m.inputBuf, pasted)
	if !m.inputBuf.PasteMode {
		t.Fatalf("expected PasteMode after pasting %d lines, box shows %q", strings.Count(pasted, "\n")+1, m.inputBuf.Value())
	}

	// Enter after the paste burst is the user's own, not another pasted newline.
	stallClock(m.inputBuf)
	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := result.(Model)

	if len(r.steered) != 1 || r.steered[0] != pasted {
		t.Fatalf("Steer = %q, want %q", r.steered, pasted)
	}
	if got := m2.lastSubmittedInput; got != pasted {
		t.Fatalf("lastSubmittedInput = %q, want %q", got, pasted)
	}

	result, _ = m2.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if got := result.(Model).inputBuf.Value(); got != pasted {
		t.Fatalf("↑ should recall the steered text, got %q", got)
	}
}

// TestUpNavigatesSuggestions: with the suggestion popup open, ↑ moves the
// selection. (The recall guard needs an empty box, and suggestions only open on
// a "/" prefix, so this pair cannot collide — TestUpPrefersOptionsPopupNavigation
// covers the case that can.)
func TestUpNavigatesSuggestions(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.width, m.height = 80, 40
	m.lastSubmittedInput = "之前提交的内容"

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m2 := result.(Model)
	if !m2.showSuggestions || len(m2.suggestions) == 0 {
		t.Fatalf("typing / should open suggestions, got %d", len(m2.suggestions))
	}
	want := len(m2.suggestions) - 1

	result, _ = m2.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	m3 := result.(Model)
	if got := m3.inputBuf.Value(); got != "/" {
		t.Fatalf("↑ with the suggestion popup open must not recall history, got %q", got)
	}
	if m3.selectedSuggestion != want {
		t.Fatalf("↑ should move the suggestion selection to %d, got %d", want, m3.selectedSuggestion)
	}
}

// TestUpPrefersOptionsPopupNavigation pins the ordering the recall block relies
// on: the ask_user options popup can be open while the box is EMPTY — exactly
// the state recall fires in — so ↑ must move the option selection instead.
func TestUpPrefersOptionsPopupNavigation(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.width, m.height = 80, 40
	m.lastSubmittedInput = "之前提交的内容"
	m.activeOptions = []string{"方案一", "方案二", "输入你的意见"}

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	m2 := result.(Model)

	if want := len(m2.activeOptions) - 1; m2.selectedOption != want {
		t.Errorf("↑ should move the option selection to %d, got %d", want, m2.selectedOption)
	}
	if got := m2.inputBuf.Value(); got != "" {
		t.Errorf("↑ with the options popup open must not recall history, got %q", got)
	}
}
