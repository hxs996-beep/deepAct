package engine

import (
	"context"
	"errors"
)

// errBoom is a sentinel error for tests that assert error propagation.
var errBoom = errors.New("boom")

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

// mockSimpleAgent implements Agent for tests that need a scripted sub-agent
// (collab pipeline, debate arena). Returns a fixed response.
type mockSimpleAgent struct {
	id       AgentID
	response string
}

func (m *mockSimpleAgent) ID() AgentID { return m.id }
func (m *mockSimpleAgent) Spec() AgentSpec {
	return AgentSpec{ID: m.id, Description: "mock agent for testing"}
}
func (m *mockSimpleAgent) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	return &HandoffResult{
		Summary:     m.response,
		Conclusions: []string{m.response},
	}, nil
}
func (m *mockSimpleAgent) SetOnProgress(fn ProgressFunc) {}

// mockPromptRunner supports RunWithPrompt for tests that drive a pipeline
// with an explicit role prompt (collab stages, debate rounds).
type mockPromptRunner struct {
	mockSimpleAgent
}

func (m *mockPromptRunner) RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	return &HandoffResult{
		Summary:     m.response,
		Conclusions: []string{m.response},
	}, nil
}
