package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMessageMarshal_AssistantAlwaysCarriesContent: an assistant message must
// always serialize a `content` field, even when empty. A model turn that
// produced only reasoning has an empty Content; with `omitempty` the field used
// to disappear and the API rejected the whole request with
// 400 "Invalid assistant message: content or tool_calls must be set".
func TestMessageMarshal_AssistantAlwaysCarriesContent(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
	}{
		{"reasoning only", Message{Role: "assistant", ReasoningContent: "thinking"}},
		{"completely empty", Message{Role: "assistant"}},
		{"with tool calls", Message{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: FunctionCall{Name: "read"}},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(b, &raw); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if _, ok := raw["content"]; !ok {
				t.Fatalf("assistant message lost its content field: %s", b)
			}
			if _, ok := raw["reasoning_content"]; tc.msg.ReasoningContent != "" && !ok {
				t.Errorf("reasoning_content was dropped: %s", b)
			}
		})
	}
}

// TestMessageMarshal_ContentSurvivesRoundTrip: the forced field must not
// disturb normal decoding (the session/response paths unmarshal into Message).
func TestMessageMarshal_ContentSurvivesRoundTrip(t *testing.T) {
	in := Message{Role: "assistant", Content: "答案", ReasoningContent: "思考"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Message
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Role != in.Role || out.Content != in.Content || out.ReasoningContent != in.ReasoningContent {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
}

// TestBuildRequestBody_ReasoningOnlyAssistantStaysValid is the wire-level
// contract: a history holding a reasoning-only assistant turn must still emit a
// schema-valid `content` field.
func TestBuildRequestBody_ReasoningOnlyAssistantStaysValid(t *testing.T) {
	body, err := (&DeepSeekClient{}).buildRequestBody(ChatRequest{
		Model: "deepseek-v4-flash",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ReasoningContent: "让我想想……"},
		},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}
	if !strings.Contains(string(body), `"content":""`) {
		t.Errorf("assistant turn without text must still send content:\"\", got %s", body)
	}
}
