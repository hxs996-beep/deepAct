package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deepact/deepact/engine"
)

// recordRunner 记录 Run 的 prompt，供断言 /confirm N 是否被发送。
type recordRunner struct {
	prompts []string
}

func (r *recordRunner) Run(prompt string, runSeq uint64) tea.Cmd {
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
		"按报告执行",
		"输入你的意见",
	}
	m.selectedOption = 0 // 选中第一项（按报告执行）
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

// 选末项"输入你的意见" Enter → 不发引擎命令，回到输入框（activeOptions 清空）。
// 允许渲染命令 repaintCmd（触发 WindowSizeMsg 全量重绘，清除弹窗背景残留）——
// 它返回 WindowSizeMsg，与引擎命令可区分。
func TestOptionsEnter_LastItemReturnsToInput(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.activeOptions = []string{"按报告执行", "输入你的意见"}
	m.selectedOption = 1 // 末项

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(Model)
	if !ok {
		t.Fatalf("expected Model from Update, got %T", got)
	}
	if len(gotModel.activeOptions) != 0 {
		t.Errorf("activeOptions should be cleared, got %v", gotModel.activeOptions)
	}
	// 末项只关闭弹窗回输入框：不得发送引擎命令（/confirm N）。
	// repaintCmd 是渲染命令（返回 WindowSizeMsg），允许。
	if cmd != nil {
		msg := cmd()
		if _, isRepaint := msg.(tea.WindowSizeMsg); !isRepaint && msg != nil {
			t.Errorf("expected no engine command on free-input last item; got %T", msg)
		}
	}
}

// 选择记录：列出全部候选并标出所选项。
func TestNewChoiceRecord_ListsCandidatesAndChoice(t *testing.T) {
	rec := newChoiceRecord([]string{"用 Redis 缓存", "改用 MySQL", "输入你的意见"}, 2)
	if rec.Role != "choice" {
		t.Fatalf("role = %q, want %q", rec.Role, "choice")
	}
	for _, want := range []string{"✓ 已选择 [2]", "改用 MySQL", "候选：", "[1] 用 Redis 缓存", "[3] 输入你的意见"} {
		if !strings.Contains(rec.Content, want) {
			t.Errorf("record missing %q; content=%q", want, rec.Content)
		}
	}
}

// 越界编号不得索引候选：只输出序号，不 panic（候选行仍按原样列出）。
func TestNewChoiceRecord_OutOfRange(t *testing.T) {
	rec := newChoiceRecord([]string{"a", "b"}, 5)
	if !strings.Contains(rec.Content, "✓ 已选择 [5]") {
		t.Errorf("out-of-range header missing, got %q", rec.Content)
	}
	if strings.Contains(rec.Content, "✓ 已选择 [5] ") {
		t.Errorf("out-of-range must not append an option label, got %q", rec.Content)
	}
}

// 渲染不 panic，且两行内容输出为两行。
func TestRenderMessage_ChoiceRecord(t *testing.T) {
	rec := newChoiceRecord([]string{"用 Redis 缓存", "改用 MySQL"}, 2)
	lines := renderMessage(rec, 80)
	if len(lines) != 2 {
		t.Errorf("expected 2 rendered lines, got %d: %v", len(lines), lines)
	}
}

// 选中方案后应在消息流追加一条 "choice" 记录，且候选来自清空前的 activeOptions。
func TestOptionsEnter_AppendsChoiceRecord(t *testing.T) {
	rr := &recordRunner{}
	m := NewModel(rr, engine.PricingConfig{})
	m.state = stateReady
	m.activeOptions = []string{"用 Redis 缓存", "改用 MySQL", "输入你的意见"}
	m.selectedOption = 0
	m.progressChan = make(chan ProgressMsg, 1)
	m.progressChan <- ProgressMsg{Type: "done"}

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(Model)
	if !ok {
		t.Fatalf("expected Model from Update, got %T", got)
	}
	if cmd == nil {
		t.Fatal("expected a command for option selection")
	}
	cmd()

	found := false
	for _, msg := range gotModel.messages {
		if msg.Role != "choice" {
			continue
		}
		found = true
		for _, want := range []string{"✓ 已选择 [1]", "用 Redis 缓存", "候选：", "[2] 改用 MySQL", "[3] 输入你的意见"} {
			if !strings.Contains(msg.Content, want) {
				t.Errorf("choice record missing %q; content=%q", want, msg.Content)
			}
		}
	}
	if !found {
		t.Fatalf("expected a choice record in the message stream, got %+v", gotModel.messages)
	}
}

// 末项"输入你的意见"是自由输入入口，不得追加选择记录（回归防护）。
func TestOptionsEnter_LastItemNoChoiceRecord(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.activeOptions = []string{"按报告执行", "输入你的意见"}
	m.selectedOption = 1

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(Model)
	if !ok {
		t.Fatalf("expected Model from Update, got %T", got)
	}
	for _, msg := range gotModel.messages {
		if msg.Role == "choice" {
			t.Errorf("the free-input last item must not append a choice record, got %+v", msg)
		}
	}
}
