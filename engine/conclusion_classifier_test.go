package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubCompleteModel is a controllable ModelClient stub: Complete returns preset
// content or error, and captures the last request for assertions. Stream is
// unused by this test suite.
type stubCompleteModel struct {
	resp      string
	reasoning string
	err       error
	last      ModelRequest
}

func (m *stubCompleteModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *stubCompleteModel) Complete(_ context.Context, req ModelRequest) (*ModelResponse, error) {
	m.last = req
	if m.err != nil {
		return nil, m.err
	}
	return &ModelResponse{Message: ModelMessage{Content: m.resp, ReasoningContent: m.reasoning}}, nil
}

func TestConclusionClassifier_IsConclusion_True(t *testing.T) {
	m := &stubCompleteModel{resp: `{"conclusion": true}`}
	c := NewConclusionClassifier(m, "flash-model", true)
	ok, err := c.IsConclusion(context.Background(), ConclusionCheck{Goal: "修复 turn.go 的 bug", Text: "任务已完成，测试全部通过。"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ok {
		t.Errorf("expected conclusion=true, got false")
	}
}

func TestConclusionClassifier_IsConclusion_False(t *testing.T) {
	m := &stubCompleteModel{resp: `{"conclusion": false}`}
	c := NewConclusionClassifier(m, "flash-model", true)
	ok, err := c.IsConclusion(context.Background(), ConclusionCheck{Goal: "修复 turn.go 的 bug", Text: "上述修改已写入 turn.go。需要验证测试结果。"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok {
		t.Errorf("expected conclusion=false for mid-task narration, got true")
	}
}

func TestConclusionClassifier_BadJSON_ReturnsError(t *testing.T) {
	m := &stubCompleteModel{resp: `not json`}
	c := NewConclusionClassifier(m, "flash-model", true)
	_, err := c.IsConclusion(context.Background(), ConclusionCheck{Goal: "goal", Text: "text"})
	if err == nil {
		t.Fatalf("expected error for non-JSON response, got nil")
	}
}

func TestConclusionClassifier_CallError_ReturnsError(t *testing.T) {
	m := &stubCompleteModel{err: errBoom}
	c := NewConclusionClassifier(m, "flash-model", true)
	_, err := c.IsConclusion(context.Background(), ConclusionCheck{Goal: "goal", Text: "text"})
	if err == nil {
		t.Fatalf("expected error from Complete, got nil")
	}
}

func TestConclusionClassifier_RequestShape(t *testing.T) {
	m := &stubCompleteModel{resp: `{"conclusion": true}`}
	c := NewConclusionClassifier(m, "flash-model", false)
	_, _ = c.IsConclusion(context.Background(), ConclusionCheck{Goal: "fix the bug", Text: "Done, tests pass."})
	req := m.last
	if req.Model != "flash-model" {
		t.Errorf("expected Model=flash-model, got %q", req.Model)
	}
	if !req.JsonMode {
		t.Errorf("expected JsonMode=true")
	}
	if req.Temperature != 0 {
		t.Errorf("expected Temperature=0, got %v", req.Temperature)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Errorf("expected system+user messages, got %+v", req.Messages)
	}
	if !strings.Contains(req.Messages[1].Content, "fix the bug") || !strings.Contains(req.Messages[1].Content, "Done, tests pass.") {
		t.Errorf("expected user message to contain goal and text, got %q", req.Messages[1].Content)
	}
}

func TestConclusionClassifier_ToolCallSummaryInPrompt(t *testing.T) {
	m := &stubCompleteModel{resp: `{"conclusion": false}`}
	c := NewConclusionClassifier(m, "flash-model", true)
	_, _ = c.IsConclusion(context.Background(), ConclusionCheck{
		Goal:            "查找关键字匹配",
		Text:            "发现了多处可能的关键字匹配。",
		ToolCallSummary: "grep×3, read×2",
	})
	if !strings.Contains(m.last.Messages[1].Content, "grep×3, read×2") {
		t.Errorf("expected tool call summary in prompt, got %q", m.last.Messages[1].Content)
	}
}

// TestConclusionClassifier_ParsesNonPureJSON reproduces the production bug:
// glm-5.2 ignores JsonMode and wraps the JSON in markdown fences or surrounds
// it with explanation text. The classifier must extract the {...} and parse it
// instead of failing with "unexpected end of JSON input" (which degraded the
// stop hook to a classifier_error block on every text-only turn).
func TestConclusionClassifier_ParsesNonPureJSON(t *testing.T) {
	tests := []struct {
		name string
		resp string
		want bool
	}{
		{"markdown wrapped true", "```json\n{\"conclusion\": true}\n```", true},
		{"markdown wrapped false", "```json\n{\"conclusion\": false}\n```", false},
		{"prefix text then json", "根据分析，结论如下：\n{\"conclusion\": true}", true},
		{"suffix text after json", "{\"conclusion\": false}\n以上是判定。", false},
		{"json with leading spaces", "   {\"conclusion\": true}   ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &stubCompleteModel{resp: tt.resp}
			c := NewConclusionClassifier(m, "flash-model", true)
			got, err := c.IsConclusion(context.Background(), ConclusionCheck{Goal: "g", Text: "t"})
			if err != nil {
				t.Fatalf("unexpected err for %s: %v (resp=%q)", tt.name, err, tt.resp)
			}
			if got != tt.want {
				t.Errorf("%s: got %v, want %v (resp=%q)", tt.name, got, tt.want, tt.resp)
			}
		})
	}
}

var errBoom = errors.New("boom")

// TestConclusionClassifier_FallsBackToReasoningContent reproduces the
// production bug: glm-5.2 occasionally returns JSON in reasoning_content with
// empty Content (llm/deepseek.go:460 "model returned only reasoning_content
// with no visible output"). The classifier only read Content, got "",
// failed with "no valid JSON in \"\"", and degraded to a classifier_error
// fallback - causing unstable stop-hook decisions. It must fall back to
// reasoning_content.
func TestConclusionClassifier_FallsBackToReasoningContent(t *testing.T) {
	m := &stubCompleteModel{resp: "", reasoning: "```json\n{\"conclusion\": true}\n```"}
	c := NewConclusionClassifier(m, "flash-model", true)
	ok, err := c.IsConclusion(context.Background(), ConclusionCheck{Goal: "g", Text: "t"})
	if err != nil {
		t.Fatalf("unexpected err (should fall back to reasoning_content): %v", err)
	}
	if !ok {
		t.Errorf("expected conclusion=true from reasoning_content fallback, got false")
	}
}
