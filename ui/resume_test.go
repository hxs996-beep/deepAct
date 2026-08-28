package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deepact/deepact/engine"
)

// mockResumeRunner 实现 EngineRunner（含新方法），供 /resume 选择器测试。
type mockResumeRunner struct {
	sessionID string
	history   []engine.Message
	sessions  []SessionSummary
}

func (m *mockResumeRunner) Run(prompt string) tea.Cmd            { return nil }
func (m *mockResumeRunner) Cancel()                              {}
func (m *mockResumeRunner) SetProgressChan(ch chan ProgressMsg)  {}
func (m *mockResumeRunner) ValidateConnection() error            { return nil }
func (m *mockResumeRunner) Steer(msg string)                     {}
func (m *mockResumeRunner) SetSessionID(id string)               { m.sessionID = id }
func (m *mockResumeRunner) SetHistory(messages []engine.Message) { m.history = messages }
func (m *mockResumeRunner) ListSessions() []SessionSummary       { return m.sessions }
func (m *mockResumeRunner) LoadHistory(id string) []engine.Message {
	return []engine.Message{{Role: "user", Content: "历史问题"}}
}

// TestEngineRunnerInterface ensures the mock satisfies the full interface.
func TestEngineRunnerInterface(t *testing.T) {
	var _ EngineRunner = (*mockResumeRunner)(nil)
}

// TestResumeCommandEnterPicker verifies /resume enters the picker state.
// 直接调用 submitInput()（不经过 handleKey 的 Enter 路径），避免 macOS
// Option/Shift 物理键检测在测试环境下的不确定性。
func TestResumeCommandEnterPicker(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.engine = &mockResumeRunner{sessions: []SessionSummary{
		{ID: "sess-1", FirstMsg: "之前的问题", EventCount: 3},
	}}
	m.state = stateReady
	m.height = 40
	m.width = 80

	// 输入 /resume 并提交
	m.inputBuf.SetValue("/resume")
	result, _ := m.submitInput()
	m2 := result.(Model)
	if m2.state != stateResume {
		t.Fatalf("state = %v, want stateResume", m2.state)
	}
	if len(m2.resumeSessions) != 1 || m2.resumeSessions[0].ID != "sess-1" {
		t.Fatalf("resumeSessions = %+v, want sess-1", m2.resumeSessions)
	}
}

// TestResumePickerNavigateAndEnter verifies ↑↓ navigation and Enter applies resume.
func TestResumePickerNavigateAndEnter(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	r := &mockResumeRunner{sessions: []SessionSummary{
		{ID: "sess-1", FirstMsg: "问题1", EventCount: 2},
		{ID: "sess-2", FirstMsg: "问题2", EventCount: 4},
	}}
	m.engine = r
	m.state = stateResume
	m.resumeSessions = r.sessions
	m.height = 40
	m.width = 80

	// Down 到第 2 个
	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	m2 := result.(Model)
	if m2.selectedResume != 1 {
		t.Fatalf("selectedResume = %d, want 1", m2.selectedResume)
	}

	// Enter 恢复第 2 个
	result, _ = m2.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m3 := result.(Model)
	if r.sessionID != "sess-2" {
		t.Errorf("runner sessionID = %q, want sess-2", r.sessionID)
	}
	if m3.state != stateReady {
		t.Errorf("state = %v, want stateReady", m3.state)
	}
	// 预填了 system 提示 + 历史消息
	found := false
	for _, msg := range m3.messages {
		if msg.Role == "user" && msg.Content == "历史问题" {
			found = true
		}
	}
	if !found {
		t.Errorf("resumed history not shown in messages: %+v", m3.messages)
	}
}

// TestResumePickerEscCancel verifies Esc exits the picker.
func TestResumePickerEscCancel(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.engine = &mockResumeRunner{sessions: []SessionSummary{{ID: "sess-1"}}}
	m.state = stateResume
	m.resumeSessions = []SessionSummary{{ID: "sess-1"}}
	m.height = 40
	m.width = 80

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := result.(Model)
	if m2.state != stateReady {
		t.Fatalf("state = %v, want stateReady after Esc", m2.state)
	}
	if m2.resumeSessions != nil {
		t.Errorf("resumeSessions = %+v, want nil after Esc", m2.resumeSessions)
	}
}

// TestResumeNoSessions verifies /resume with empty list shows a notice.
func TestResumeNoSessions(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.engine = &mockResumeRunner{sessions: nil}
	m.state = stateReady
	m.height = 40
	m.width = 80
	m.inputBuf.SetValue("/resume")
	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := result.(Model)
	if m2.state == stateResume {
		t.Fatalf("state = %v, want stay ready with notice", m2.state)
	}
}
