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
