package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deepact/deepact/engine"
)

// recordRunner 记录 Run 的 prompt，供断言 /confirm N 是否被发送。
type recordRunner struct {
	prompts []string
}

func (r *recordRunner) Run(prompt string) tea.Cmd {
	r.prompts = append(r.prompts, prompt)
	return func() tea.Msg { return nil }
}
func (r *recordRunner) Cancel()                                {}
func (r *recordRunner) SetProgressChan(ch chan ProgressMsg)    {}
func (r *recordRunner) ValidateConnection() error              { return nil }
func (r *recordRunner) Steer(msg string)                       {}
func (r *recordRunner) SetSessionID(id string)                 {}
func (r *recordRunner) SetHistory(messages []engine.Message)   {}
func (r *recordRunner) ListSessions() []SessionSummary         { return nil }
func (r *recordRunner) LoadHistory(id string) []engine.Message { return nil }

// 选方案（非末项）Enter → 发送内部 /confirm N 命令，不写入输入框。
// 注意：Update 与 handleKey 均为值接收者（ui/model.go:249, 949），
// 修改发生在返回的 tea.Model 副本上，断言必须从 got 提取 Model。
func TestOptionsEnter_SendsConfirmCommand(t *testing.T) {
	rr := &recordRunner{}
	m := NewModel(rr, engine.PricingConfig{})
	m.state = stateReady
	m.activeOptions = []string{
		"方案A: 按报告执行修改",
		"方案B: 调整方案后执行",
		"方案C: 取消本次修改",
		"其他（输入你的意见）",
	}
	m.selectedOption = 0 // 方案A
	// 让 submitConfirm 启动路径中的 waitForProgress 不阻塞：预填一条消息。
	m.progressChan = make(chan ProgressMsg, 1)
	m.progressChan <- ProgressMsg{Type: "done"}

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a command for option selection")
	}
	gotModel, ok := got.(Model)
	if !ok {
		t.Fatalf("expected Model from Update, got %T", got)
	}
	cmd() // 执行命令，触发 recordRunner.Run 与 waitForProgress
	if len(rr.prompts) != 1 || rr.prompts[0] != "/confirm 1" {
		t.Errorf("expected Run(\"/confirm 1\"), got prompts=%v", rr.prompts)
	}
	if gotModel.inputBuf.Value() != "" {
		t.Errorf("input buffer should NOT be filled with the option number, got %q", gotModel.inputBuf.Value())
	}
	if len(gotModel.activeOptions) != 0 {
		t.Errorf("activeOptions should be cleared, got %v", gotModel.activeOptions)
	}
}

// 选末项"其他" Enter → 不发命令，回到输入框（activeOptions 清空）。
func TestOptionsEnter_LastItemReturnsToInput(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.activeOptions = []string{"方案A", "方案B", "其他（输入你的意见）"}
	m.selectedOption = 2 // 末项

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("expected NO command when selecting the free-input last item")
	}
	gotModel, ok := got.(Model)
	if !ok {
		t.Fatalf("expected Model from Update, got %T", got)
	}
	if len(gotModel.activeOptions) != 0 {
		t.Errorf("activeOptions should be cleared, got %v", gotModel.activeOptions)
	}
}
