package llm

import (
	"errors"
	"fmt"
	"testing"

	"github.com/deepact/deepact/engine"
)

func TestIsContextLengthError(t *testing.T) {
	yes := []string{
		`{"error":{"message":"This model maximum context length is 1048576 tokens. However, you requested 3780243 tokens"}}`,
		`context_length_exceeded`,
		`Please reduce the length of the messages.`,
	}
	for _, s := range yes {
		if !isContextLengthError([]byte(s)) {
			t.Errorf("isContextLengthError(%q) = false, want true", s)
		}
	}
	no := []string{
		`{"error":{"message":"insufficient tool messages following tool_calls message"}}`,
		`invalid api key`,
	}
	for _, s := range no {
		if isContextLengthError([]byte(s)) {
			t.Errorf("isContextLengthError(%q) = true, want false", s)
		}
	}
}

// TestMapModelError_ContextLength 验证 context-length 哨兵会被映射到 engine
// 层，让主/子 agent 能识别并做裁剪重试。
func TestMapModelError_ContextLength(t *testing.T) {
	err := mapModelError(fmt.Errorf("status 400: boom: %w", ErrContextLength))
	if !errors.Is(err, engine.ErrModelContextLength) {
		t.Fatalf("mapModelError must map ErrContextLength to engine.ErrModelContextLength, got %v", err)
	}
}
