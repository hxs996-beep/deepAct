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
	resp string
	err  error
	last ModelRequest
}

func (m *stubCompleteModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *stubCompleteModel) Complete(_ context.Context, req ModelRequest) (*ModelResponse, error) {
	m.last = req
	if m.err != nil {
		return nil, m.err
	}
	return &ModelResponse{Message: ModelMessage{Content: m.resp}}, nil
}

func TestConclusionClassifier_IsConclusion_True(t *testing.T) {
	m := &stubCompleteModel{resp: `{"conclusion": true}`}
	c := NewConclusionClassifier(m, "flash-model", true)
	ok, err := c.IsConclusion(context.Background(), "修复 turn.go 的 bug", "任务已完成，测试全部通过。")
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
	ok, err := c.IsConclusion(context.Background(), "修复 turn.go 的 bug", "上述修改已写入 turn.go。下面运行测试验证。")
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
	_, err := c.IsConclusion(context.Background(), "goal", "text")
	if err == nil {
		t.Fatalf("expected error for non-JSON response, got nil")
	}
}

func TestConclusionClassifier_CallError_ReturnsError(t *testing.T) {
	m := &stubCompleteModel{err: errBoom}
	c := NewConclusionClassifier(m, "flash-model", true)
	_, err := c.IsConclusion(context.Background(), "goal", "text")
	if err == nil {
		t.Fatalf("expected error from Complete, got nil")
	}
}

func TestConclusionClassifier_RequestShape(t *testing.T) {
	m := &stubCompleteModel{resp: `{"conclusion": true}`}
	c := NewConclusionClassifier(m, "flash-model", false)
	_, _ = c.IsConclusion(context.Background(), "fix the bug", "Done, tests pass.")
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

var errBoom = errors.New("boom")
