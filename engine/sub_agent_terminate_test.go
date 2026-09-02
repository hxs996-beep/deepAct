package engine

import (
	"context"
	"strings"
	"testing"
)

// stubSeqModel returns scripted ModelResponses in sequence on each Complete
// call, cycling back to the first once exhausted. Sub-agents use Complete (not
// Stream), so Stream is a no-op. The cycling simulates a model that alternates
// between tool calls and text.
type stubSeqModel struct {
	responses       []ModelResponse
	classifierResp  string // returned on JsonMode=true calls (ConclusionClassifier probes)
	calls           int    // counts non-classifier (scripted) calls only
	classifierCalls int    // counts ConclusionClassifier (JsonMode) probes
	lastReq         ModelRequest // most recent request, for tool-spec assertions
}

func (m *stubSeqModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *stubSeqModel) Complete(_ context.Context, req ModelRequest) (*ModelResponse, error) {
	m.lastReq = req
	// ConclusionClassifier probes use JsonMode; return the scripted verdict
	// without advancing the scripted call counter.
	if req.JsonMode {
		m.classifierCalls++
		if m.classifierResp == "" {
			return &ModelResponse{}, nil
		}
		return &ModelResponse{Message: ModelMessage{Content: m.classifierResp}}, nil
	}
	if len(m.responses) == 0 {
		m.calls++
		return &ModelResponse{}, nil
	}
	resp := m.responses[m.calls%len(m.responses)]
	m.calls++
	return &resp, nil
}

// TestSubAgentRunLoop_NudgesOnNextStepNarration ensures a forward-looking
// narration (no tool calls) gets nudged rather than treated as a conclusion.
func TestSubAgentRunLoop_NudgesOnNextStepNarration(t *testing.T) {
	narration := ModelResponse{
		Message: ModelMessage{
			Role:    "assistant",
			Content: "查看 finishStreaming 逻辑，确认截断点。",
		},
		FinishReason: "stop",
	}
	model := &stubSeqModel{responses: []ModelResponse{narration}, classifierResp: `{"conclusion": false}`}

	runner := &SubAgentRunner{
		model:     model,
		tools:     stubToolExecutor{},
		modelName: "test",
	}

	result, err := runner.Run(context.Background(), Handoff{
		Agent:         AgentSub,
		Goal:          "分析截断问题",
		MaxIterations: 8,
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	// Narration cycles: 3 consecutive text-only turns -> break (3-strike).
	// It must NOT terminate on the first turn.
	if model.calls < 3 {
		t.Errorf("expected narration to be nudged (>=3 calls via 3-strike), got %d - over-terminated", model.calls)
	}
	if !strings.Contains(result.Summary, "截断") {
		t.Errorf("expected Summary to retain the narration content, got: %q", result.Summary)
	}
}
