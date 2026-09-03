package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPresentOptionsToolSpec(t *testing.T) {
	spec := presentOptionsToolSpec(true)
	if spec.Function.Name != PresentOptionsToolName {
		t.Errorf("name = %q, want %q", spec.Function.Name, PresentOptionsToolName)
	}
	if spec.Function.Description == "" {
		t.Error("description should not be empty")
	}
	var params struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties struct {
			Options struct {
				Type     string `json:"type"`
				MinItems int    `json:"minItems"`
				MaxItems int    `json:"maxItems"`
				Items    struct {
					Type      string `json:"type"`
					MinLength int    `json:"minLength"`
				} `json:"items"`
			} `json:"options"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(spec.Function.Parameters, &params); err != nil {
		t.Fatalf("unmarshal parameters: %v", err)
	}
	if len(params.Required) != 1 || params.Required[0] != "options" {
		t.Errorf("required = %v, want [options]", params.Required)
	}
	if params.Properties.Options.MinItems != 2 {
		t.Errorf("minItems = %d, want 2", params.Properties.Options.MinItems)
	}
	if params.Properties.Options.MaxItems != 6 {
		t.Errorf("maxItems = %d, want 6", params.Properties.Options.MaxItems)
	}
	if params.Properties.Options.Items.MinLength != 1 {
		t.Errorf("items.minLength = %d, want 1", params.Properties.Options.Items.MinLength)
	}
	if params.Properties.Options.Items.Type != "string" {
		t.Errorf("items.type = %q, want string", params.Properties.Options.Items.Type)
	}
}

func TestProcessPresentOptionsCalls_CapturesValid(t *testing.T) {
	e := &Engine{}
	msgs := e.processPresentOptionsCalls([]ToolCallRequest{
		{ID: "call_opt", Name: PresentOptionsToolName, Input: json.RawMessage(
			`{"options":["用 Redis 缓存","改用 MySQL"]}`)},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if msgs[0].Content != "✓ 已记录 2 个可选方案，等待用户选择。" {
		t.Errorf("tool response = %q", msgs[0].Content)
	}
	if len(e.pendingConfirmOptions) != 2 || e.pendingConfirmOptions[1] != "改用 MySQL" {
		t.Errorf("pendingConfirmOptions = %v, want [用 Redis 缓存 改用 MySQL]", e.pendingConfirmOptions)
	}
}

func TestProcessPresentOptionsCalls_RejectsInvalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty array", `{"options":[]}`},
		{"single option", `{"options":["only one"]}`},
		{"too many", `{"options":["a","b","c","d","e","f","g"]}`},
		{"blank item", `{"options":["a","  "]} `},
		{"bad json", `{invalid}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &Engine{}
			msgs := e.processPresentOptionsCalls([]ToolCallRequest{
				{ID: "call_opt", Name: PresentOptionsToolName, Input: json.RawMessage(c.input)},
			})
			if len(msgs) != 1 {
				t.Fatalf("expected 1 error response, got %d", len(msgs))
			}
			if msgs[0].Content[:6] != "Error:" {
				t.Errorf("expected Error response, got %q", msgs[0].Content)
			}
			if len(e.pendingConfirmOptions) != 0 {
				t.Errorf("pendingConfirmOptions should stay empty, got %v", e.pendingConfirmOptions)
			}
		})
	}
}

func TestToolSpecsWithHandoff_IncludesPresentOptions(t *testing.T) {
	e := &Engine{tools: stubToolExecutor{}, isChinese: true}
	specs := e.toolSpecsWithHandoff()
	found := false
	for _, s := range specs {
		if s.Function.Name == PresentOptionsToolName {
			found = true
			break
		}
	}
	if !found {
		t.Error("toolSpecsWithHandoff should include present_options")
	}
}

func TestProcessPresentOptionsCalls_IgnoresOtherTools(t *testing.T) {
	e := &Engine{}
	msgs := e.processPresentOptionsCalls([]ToolCallRequest{
		{ID: "call_grep", Name: "grep", Input: json.RawMessage(`{}`)},
	})
	if len(msgs) != 0 {
		t.Errorf("expected no responses for non-present_options, got %d", len(msgs))
	}
	if len(e.pendingConfirmOptions) != 0 {
		t.Errorf("pendingConfirmOptions should stay empty, got %v", e.pendingConfirmOptions)
	}
}

func TestConfirmOptions_NoDeclaredOptions_FixedTwo(t *testing.T) {
	e := &Engine{}
	got := e.confirmOptions()
	want := []string{"按报告执行", "输入你的意见"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("option %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestConfirmOptions_DeclaredOptions_ABCPrefixed(t *testing.T) {
	e := &Engine{pendingConfirmOptions: []string{"用 Redis 缓存", "改用 MySQL"}}
	got := e.confirmOptions()
	want := []string{"方案A: 用 Redis 缓存", "方案B: 改用 MySQL", "输入你的意见"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("option %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// 无声明方案时，/confirm 1 确认报告（"按报告执行"）并清除 nudge。
func TestHandleConfirmCommand_NoOptions_ConfirmExecutes(t *testing.T) {
	e := &Engine{
		state:    &TaskState{AnalysisMode: true, AnalysisReportConfirmed: false},
		history:  []Message{{Role: "user", Content: "/confirm 1"}},
		isChinese: true,
	}
	e.pendingAnalysisNudge = true

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	if e.state.AnalysisMode || !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisMode should be false / AnalysisReportConfirmed true after confirm")
	}
	if e.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after confirm")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "按报告执行") {
		t.Errorf("history should mention 按报告执行, got %q", last)
	}
}

// 有声明方案时，/confirm N 选择方案N并注入方案描述。
func TestHandleConfirmCommand_WithOptions_SelectedPlanInjected(t *testing.T) {
	e := &Engine{
		state:    &TaskState{AnalysisMode: true, AnalysisReportConfirmed: false},
		history:  []Message{{Role: "user", Content: "/confirm 2"}},
		isChinese: true,
		pendingConfirmOptions: []string{"用 Redis 缓存", "改用 MySQL"},
	}

	if !e.handleConfirmCommand("/confirm 2") {
		t.Fatal("handleConfirmCommand should handle /confirm 2")
	}
	if e.state.AnalysisMode || !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisMode should be false / AnalysisReportConfirmed true after selecting a plan")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "方案B: 改用 MySQL") {
		t.Errorf("history should mention the selected plan 方案B: 改用 MySQL, got %q", last)
	}
	if len(e.pendingConfirmOptions) != 0 {
		t.Errorf("pendingConfirmOptions should be cleared after selection, got %v", e.pendingConfirmOptions)
	}
}
