package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deepact/deepact/engine"
)

// recordingRunner records Steer calls for tests.
type recordingRunner struct {
	steered []string
}

func (r *recordingRunner) Run(prompt string, runSeq uint64) tea.Cmd     { return nil }
func (r *recordingRunner) Cancel()                              {}
func (r *recordingRunner) SetProgressChan(ch chan ProgressMsg)  {}
func (r *recordingRunner) ValidateConnection() error            { return nil }
func (r *recordingRunner) Steer(msg string)                     { r.steered = append(r.steered, msg) }
func (r *recordingRunner) SetSessionID(id string)               {}
func (r *recordingRunner) SetHistory(messages []engine.Message) {}
func (r *recordingRunner) ListSessions() []SessionSummary       { return nil }
func (r *recordingRunner) LoadHistory(id string) []engine.Message {
	return nil
}

func TestRunningStateTypingInsertsIntoInput(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateRunning
	m.height = 40
	m.width = 80

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("补充信息")})
	m2 := result.(Model)
	if got := m2.inputBuf.Value(); got != "补充信息" {
		t.Fatalf("typing during running: input = %q, want %q", got, "补充信息")
	}
}

func TestRunningStateEnterQueuesMessage(t *testing.T) {
	r := &recordingRunner{}
	m := NewModel(nil, engine.PricingConfig{})
	m.engine = r
	m.state = stateRunning
	m.height = 40
	m.width = 80
	m.inputBuf.SetValue("补充：也检查测试文件")

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := result.(Model)

	if len(r.steered) != 1 || r.steered[0] != "补充：也检查测试文件" {
		t.Fatalf("Steer calls = %v, want [补充：也检查测试文件]", r.steered)
	}
	if got := m2.inputBuf.Value(); got != "" {
		t.Fatalf("input should be cleared after submit, got %q", got)
	}
	if len(m2.messages) != 1 || !m2.messages[0].Queued || m2.messages[0].Content != "补充：也检查测试文件" {
		t.Fatalf("expected one Queued user message, got %+v", m2.messages)
	}
}

func TestRunningStateEnterEmptyDoesNothing(t *testing.T) {
	r := &recordingRunner{}
	m := NewModel(nil, engine.PricingConfig{})
	m.engine = r
	m.state = stateRunning
	m.height = 40
	m.width = 80

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := result.(Model)

	if len(r.steered) != 0 {
		t.Fatalf("Steer calls = %v, want none for empty input", r.steered)
	}
	if len(m2.messages) != 0 {
		t.Fatalf("no message should be queued for empty input, got %+v", m2.messages)
	}
}

func TestRunningStateEnterWithNilEngineNoPanic(t *testing.T) {
	// NewModel(nil, ...) leaves engine nil; Enter must not panic.
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateRunning
	m.height = 40
	m.width = 80
	m.inputBuf.SetValue("无引擎消息")

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := result.(Model); !ok {
		t.Fatal("expected Model after Enter with nil engine")
	}
}

func TestRenderInputLineRunningCursorVisible(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateRunning
	m.width = 80
	m.inputBuf.SetValue("abc")

	line := stripAnsi(renderInputLine(m))
	// Cursor is rendered as █ after the typed text.
	if !strings.Contains(line, "abc█") {
		t.Fatalf("cursor should be visible while running, got: %q", line)
	}
}
