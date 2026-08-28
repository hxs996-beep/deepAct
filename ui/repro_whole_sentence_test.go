package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deepact/deepact/engine"
)

type mockRunner struct {
	progressCh chan ProgressMsg
}

func (r *mockRunner) Run(prompt string) tea.Cmd {
	return func() tea.Msg {
		full := "按验证门要求，派出 critic代理审查本次改动"
		r.progressCh <- ProgressMsg{Type: "content_delta", Detail: full}
		time.Sleep(200 * time.Millisecond)
		return EngineResponseMsg{
			Response: &engine.EngineResponse{Summary: full},
			Err:      nil,
		}
	}
}
func (r *mockRunner) Cancel()                                {}
func (r *mockRunner) SetProgressChan(ch chan ProgressMsg)    { r.progressCh = ch }
func (r *mockRunner) ValidateConnection() error              { return nil }
func (r *mockRunner) Steer(msg string)                       {}
func (r *mockRunner) SetSessionID(id string)                 {}
func (r *mockRunner) SetHistory(messages []engine.Message)   {}
func (r *mockRunner) ListSessions() []SessionSummary         { return nil }
func (r *mockRunner) LoadHistory(id string) []engine.Message { return nil }

// TestReproWholeSentenceView checks the TEXT LAYER: a whole sentence arriving
// as one content_delta must render in correct character order in View().
func TestReproWholeSentenceView(t *testing.T) {
	m := NewModel(&mockRunner{}, engine.PricingConfig{})
	m.ready = true
	m.state = stateRunning
	m.width = 80
	m.height = 24
	m.messages = []DisplayMessage{
		{Role: "user", Content: "请审查本次改动"},
		{Role: "narration", Content: "完成初步分析，识别出 3 个风险点。"},
		{Role: "toolsummary", Content: "● 2 tools executed, 1 files modified\n  [<>] agent/coordinator.go (全文)"},
	}
	m.narration = "按验证门要求，派出 critic代理审查本次改动"
	m.flushNarration()

	out := m.View()
	plain := stripAnsi(out)
	if strings.Contains(plain, "验证要求门") {
		t.Fatalf("View() output has swapped text '验证要求门':\n%q", truncateStr(plain, 300))
	}
	if !strings.Contains(plain, "验证门要求") {
		t.Errorf("View() output missing '验证门要求':\n%q", truncateStr(plain, 300))
	}
}

// TestReproWholeSentenceStreaming checks renderStreaming directly.
func TestReproWholeSentenceStreaming(t *testing.T) {
	widths := []int{30, 40, 50, 60, 70, 78, 80, 100, 120, 160}
	for _, w := range widths {
		lines := renderStreaming("按验证门要求，派出 critic代理审查本次改动", w)
		joined := strings.Join(lines, "\n")
		if strings.Contains(joined, "验证要求门") {
			t.Errorf("width=%d renderStreaming swapped: %q", w, joined)
		}
		if !strings.Contains(joined, "验证门要求") {
			t.Errorf("width=%d renderStreaming missing: %q", w, joined)
		}
	}
}

// TestReproWholeSentenceRenderMessage checks the finalized narration message path.
func TestReproWholeSentenceRenderMessage(t *testing.T) {
	widths := []int{30, 40, 50, 60, 70, 78, 80, 100, 120, 160}
	for _, w := range widths {
		lines := renderMessage(DisplayMessage{Role: "narration", Content: "按验证门要求，派出 critic代理审查本次改动"}, w)
		joined := strings.Join(lines, "\n")
		plain := stripAnsi(joined)
		if strings.Contains(plain, "验证要求门") {
			t.Errorf("width=%d renderMessage swapped: %q", w, plain)
		}
		if !strings.Contains(plain, "验证门要求") {
			t.Errorf("width=%d renderMessage missing: %q", w, plain)
		}
	}
}
