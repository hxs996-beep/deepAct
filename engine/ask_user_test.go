package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAskUserToolSpec(t *testing.T) {
	spec := askUserToolSpec(true)
	if spec.Function.Name != AskUserToolName {
		t.Errorf("name = %q, want %q", spec.Function.Name, AskUserToolName)
	}
	if spec.Function.Description == "" {
		t.Error("description should not be empty")
	}
	var params struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties struct {
			Question struct {
				Type string `json:"type"`
			} `json:"question"`
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
	if len(params.Required) != 1 || params.Required[0] != "question" {
		t.Errorf("required = %v, want [question]", params.Required)
	}
	if params.Properties.Question.Type != "string" {
		t.Errorf("question.type = %q, want string", params.Properties.Question.Type)
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

func TestProcessAskUserCalls_CapturesValid(t *testing.T) {
	e := &Engine{}
	msgs := e.processAskUserCalls([]ToolCallRequest{
		{ID: "call_ask", Name: AskUserToolName, Input: json.RawMessage(
			`{"question":"缓存方案选哪个？","options":["用 Redis 缓存","改用 MySQL"]}`)},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if e.pendingAskUser == nil {
		t.Fatal("pendingAskUser should be set")
	}
	if e.pendingAskUser.Question != "缓存方案选哪个？" {
		t.Errorf("question = %q", e.pendingAskUser.Question)
	}
	if len(e.pendingAskUser.Options) != 2 || e.pendingAskUser.Options[1] != "改用 MySQL" {
		t.Errorf("options = %v", e.pendingAskUser.Options)
	}
}

func TestProcessAskUserCalls_CapturesNoOptions(t *testing.T) {
	e := &Engine{}
	msgs := e.processAskUserCalls([]ToolCallRequest{
		{ID: "call_ask", Name: AskUserToolName, Input: json.RawMessage(
			`{"question":"数据库连接字符串是什么？"}`)},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if e.pendingAskUser == nil || e.pendingAskUser.Question != "数据库连接字符串是什么？" {
		t.Fatalf("pendingAskUser = %+v", e.pendingAskUser)
	}
	if len(e.pendingAskUser.Options) != 0 {
		t.Errorf("options should be empty, got %v", e.pendingAskUser.Options)
	}
}

func TestProcessAskUserCalls_RejectsInvalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty question", `{"question":""}`},
		{"blank question", `{"question":"   "}`},
		{"single option", `{"question":"q","options":["only one"]}`},
		{"too many", `{"question":"q","options":["a","b","c","d","e","f","g"]}`},
		{"blank item", `{"question":"q","options":["a","  "]} `},
		{"bad json", `{invalid}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &Engine{}
			msgs := e.processAskUserCalls([]ToolCallRequest{
				{ID: "call_ask", Name: AskUserToolName, Input: json.RawMessage(c.input)},
			})
			if len(msgs) != 1 {
				t.Fatalf("expected 1 error response, got %d", len(msgs))
			}
			if len(msgs[0].Content) < 6 || msgs[0].Content[:6] != "Error:" {
				t.Errorf("expected Error response, got %q", msgs[0].Content)
			}
			if e.pendingAskUser != nil {
				t.Errorf("pendingAskUser should stay nil, got %+v", e.pendingAskUser)
			}
		})
	}
}

func TestProcessAskUserCalls_IgnoresOtherTools(t *testing.T) {
	e := &Engine{}
	msgs := e.processAskUserCalls([]ToolCallRequest{
		{ID: "call_grep", Name: "grep", Input: json.RawMessage(`{}`)},
	})
	if len(msgs) != 0 {
		t.Errorf("expected no responses for non-ask_user, got %d", len(msgs))
	}
	if e.pendingAskUser != nil {
		t.Errorf("pendingAskUser should stay nil, got %+v", e.pendingAskUser)
	}
}

func TestToolSpecsWithHandoff_IncludesAskUser(t *testing.T) {
	e := &Engine{tools: stubToolExecutor{}, isChinese: true}
	specs := e.toolSpecsWithHandoff()
	found := false
	for _, s := range specs {
		if s.Function.Name == AskUserToolName {
			found = true
			break
		}
	}
	if !found {
		t.Error("toolSpecsWithHandoff should include ask_user")
	}
}

func TestAskUserOptions_NoPending_FixedTwo(t *testing.T) {
	e := &Engine{}
	got := e.askUserOptions()
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

func TestAskUserOptions_PendingWithOptions_ABCPrefixed(t *testing.T) {
	e := &Engine{pendingAskUser: &AskUserRequest{
		Question: "缓存方案选哪个？",
		Options:  []string{"用 Redis 缓存", "改用 MySQL"},
	}}
	got := e.askUserOptions()
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

func TestAskUserOptions_PendingNoOptions_Nil(t *testing.T) {
	e := &Engine{pendingAskUser: &AskUserRequest{Question: "数据库连接字符串是什么？"}}
	got := e.askUserOptions()
	if got != nil {
		t.Errorf("expected nil options without options, got %v", got)
	}
}

// 无待决问题（纯分析门控）时，/confirm 1 确认报告（"按报告执行"）。
func TestHandleConfirmCommand_NoOptions_ConfirmExecutes(t *testing.T) {
	e := &Engine{
		state:     &TaskState{AnalysisReportConfirmed: false},
		history:   []Message{{Role: "user", Content: "/confirm 1"}},
		isChinese: true,
	}
	e.pendingAnalysisNudge = true

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after confirm")
	}
	if e.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after confirm")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "按报告执行") {
		t.Errorf("history should mention 按报告执行, got %q", last)
	}
}

// 有待决问题且带 options 时，/confirm N 选择方案N并注入方案描述。
func TestHandleConfirmCommand_WithOptions_SelectedPlanInjected(t *testing.T) {
	e := &Engine{
		state:     &TaskState{AnalysisReportConfirmed: false},
		history:   []Message{{Role: "user", Content: "/confirm 2"}},
		isChinese: true,
		pendingAskUser: &AskUserRequest{
			Question: "缓存方案选哪个？",
			Options:  []string{"用 Redis 缓存", "改用 MySQL"},
		},
	}

	if !e.handleConfirmCommand("/confirm 2") {
		t.Fatal("handleConfirmCommand should handle /confirm 2")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after selecting a plan")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "方案B: 改用 MySQL") {
		t.Errorf("history should mention the selected plan 方案B: 改用 MySQL, got %q", last)
	}
	if e.pendingAskUser != nil {
		t.Errorf("pendingAskUser should be cleared after selection, got %+v", e.pendingAskUser)
	}
}

// 有待决问题但编号越界（n > len(options)）时，不静默降级为"按报告执行"。
func TestHandleConfirmCommand_WithOptions_InvalidIndex(t *testing.T) {
	e := &Engine{
		state:     &TaskState{AnalysisReportConfirmed: false},
		history:   []Message{{Role: "user", Content: "/confirm 5"}},
		isChinese: true,
		pendingAskUser: &AskUserRequest{
			Question: "缓存方案选哪个？",
			Options:  []string{"用 Redis 缓存", "改用 MySQL"},
		},
	}

	if !e.handleConfirmCommand("/confirm 5") {
		t.Fatal("handleConfirmCommand should handle /confirm 5")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after out-of-range confirm")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "无效") {
		t.Errorf("history should mention invalid option for out-of-range N, got %q", last)
	}
	if strings.Contains(last, "按报告执行") {
		t.Errorf("out-of-range N must NOT degrade to 按报告执行, got %q", last)
	}
	if e.pendingAskUser != nil {
		t.Errorf("pendingAskUser should be cleared after out-of-range confirm, got %+v", e.pendingAskUser)
	}
}
