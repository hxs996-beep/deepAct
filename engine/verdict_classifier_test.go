package engine

import (
	"context"
	"strings"
	"testing"
)

// TestVerdictClassifier_Classify_Question verifies that a reply asking the
// user a question (e.g. "方案1、2、3 你选哪个？") is classified as
// VerdictQuestion — the engine must stop and wait for the user.
func TestVerdictClassifier_Classify_Question(t *testing.T) {
	m := &stubCompleteModel{resp: `{"verdict": "question"}`}
	c := NewConclusionClassifier(m, "flash-model", true)
	v, err := c.Classify(context.Background(), ConclusionCheck{
		Goal: "修复 turn.go 的 bug",
		Text: "这里有三种修复方案：方案1 改 turn.go，方案2 改 loop.go，方案3 都改。你选哪个？",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if v != VerdictQuestion {
		t.Errorf("expected VerdictQuestion, got %v", v)
	}
}

// TestVerdictClassifier_Classify_Conclusion verifies conclusion classification.
func TestVerdictClassifier_Classify_Conclusion(t *testing.T) {
	m := &stubCompleteModel{resp: `{"verdict": "conclusion"}`}
	c := NewConclusionClassifier(m, "flash-model", true)
	v, err := c.Classify(context.Background(), ConclusionCheck{
		Goal: "修复 turn.go 的 bug",
		Text: "任务已完成，测试全部通过。",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if v != VerdictConclusion {
		t.Errorf("expected VerdictConclusion, got %v", v)
	}
}

// TestVerdictClassifier_Classify_Intermediate verifies intermediate narration.
func TestVerdictClassifier_Classify_Intermediate(t *testing.T) {
	m := &stubCompleteModel{resp: `{"verdict": "intermediate"}`}
	c := NewConclusionClassifier(m, "flash-model", true)
	v, err := c.Classify(context.Background(), ConclusionCheck{
		Goal: "修复 turn.go 的 bug",
		Text: "上述修改已写入 turn.go。下面运行测试验证。",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if v != VerdictIntermediate {
		t.Errorf("expected VerdictIntermediate, got %v", v)
	}
}

// TestVerdictClassifier_Classify_Error verifies that a model/network error
// returns VerdictIntermediate (conservative: never a question, never a
// conclusion) plus the error.
func TestVerdictClassifier_Classify_Error(t *testing.T) {
	m := &stubCompleteModel{err: errBoom}
	c := NewConclusionClassifier(m, "flash-model", true)
	v, err := c.Classify(context.Background(), ConclusionCheck{Goal: "g", Text: "t"})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if v != VerdictIntermediate {
		t.Errorf("expected VerdictIntermediate on error, got %v", v)
	}
}

// TestVerdictClassifier_Classify_ParsesNonPureJSON verifies the parser
// tolerates markdown-wrapped / prefixed / suffixed JSON (flash model quirk).
func TestVerdictClassifier_Classify_ParsesNonPureJSON(t *testing.T) {
	tests := []struct {
		name string
		resp string
		want TextVerdict
	}{
		{"markdown wrapped question", "```json\n{\"verdict\": \"question\"}\n```", VerdictQuestion},
		{"prefix then conclusion", "根据分析：\n{\"verdict\": \"conclusion\"}", VerdictConclusion},
		{"suffix after intermediate", "{\"verdict\": \"intermediate\"}\n以上是判定。", VerdictIntermediate},
		{"leading spaces question", "   {\"verdict\": \"question\"}   ", VerdictQuestion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &stubCompleteModel{resp: tt.resp}
			c := NewConclusionClassifier(m, "flash-model", true)
			got, err := c.Classify(context.Background(), ConclusionCheck{Goal: "g", Text: "t"})
			if err != nil {
				t.Fatalf("unexpected err for %s: %v (resp=%q)", tt.name, err, tt.resp)
			}
			if got != tt.want {
				t.Errorf("%s: got %v, want %v (resp=%q)", tt.name, got, tt.want, tt.resp)
			}
		})
	}
}

// TestVerdictClassifier_Classify_FallsBackToReasoningContent mirrors the
// IsConclusion fallback: JSON may arrive in reasoning_content with empty
// Content.
func TestVerdictClassifier_Classify_FallsBackToReasoningContent(t *testing.T) {
	m := &stubCompleteModel{resp: "", reasoning: "{\"verdict\": \"question\"}"}
	c := NewConclusionClassifier(m, "flash-model", true)
	v, err := c.Classify(context.Background(), ConclusionCheck{Goal: "g", Text: "t"})
	if err != nil {
		t.Fatalf("unexpected err (should fall back to reasoning_content): %v", err)
	}
	if v != VerdictQuestion {
		t.Errorf("expected VerdictQuestion from reasoning_content fallback, got %v", v)
	}
}

// TestVerdictClassifier_Classify_RequestShape verifies the judge uses the
// flash model with JsonMode and includes goal + text in the prompt.
func TestVerdictClassifier_Classify_RequestShape(t *testing.T) {
	m := &stubCompleteModel{resp: `{"verdict": "conclusion"}`}
	c := NewConclusionClassifier(m, "flash-model", false)
	_, _ = c.Classify(context.Background(), ConclusionCheck{Goal: "fix the bug", Text: "Done, tests pass."})
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

// TestVerdictClassifier_Classify_ToolCallSummaryInPrompt verifies tool context.
func TestVerdictClassifier_Classify_ToolCallSummaryInPrompt(t *testing.T) {
	m := &stubCompleteModel{resp: `{"verdict": "intermediate"}`}
	c := NewConclusionClassifier(m, "flash-model", true)
	_, _ = c.Classify(context.Background(), ConclusionCheck{
		Goal:            "查找问题",
		Text:            "发现了多处可能的问题。",
		ToolCallSummary: "grep×3, read×2",
	})
	if !strings.Contains(m.last.Messages[1].Content, "grep×3, read×2") {
		t.Errorf("expected tool call summary in prompt, got %q", m.last.Messages[1].Content)
	}
}

// TestVerdictClassifier_UnrecognizedVerdict_ReturnsError verifies that an
// unrecognized verdict string is rejected (never silently treated as a
// question — a false "ask the user" would stall the agent).
func TestVerdictClassifier_UnrecognizedVerdict_ReturnsError(t *testing.T) {
	m := &stubCompleteModel{resp: `{"verdict": "banana"}`}
	c := NewConclusionClassifier(m, "flash-model", true)
	_, err := c.Classify(context.Background(), ConclusionCheck{Goal: "g", Text: "t"})
	if err == nil {
		t.Fatalf("expected error for unrecognized verdict, got nil")
	}
}

// TestVerdictClassifier_PromptCoversQuestionWithPlannedAction verifies the
// system prompt explicitly instructs the judge that a reply which previews a
// next action (e.g. "确认后我就开始写代码") while asking the user a decision is
// STILL a question. Without this instruction, the flash judge misclassifies
// such replies as intermediate ("describes a next step"), the stop hook
// nudges the model to continue, and the model self-answers its own question —
// the 自问自答 bug.
func TestVerdictClassifier_PromptCoversQuestionWithPlannedAction(t *testing.T) {
	m := &stubCompleteModel{resp: `{"verdict": "question"}`}
	c := NewConclusionClassifier(m, "flash-model", true)
	_, _ = c.Classify(context.Background(), ConclusionCheck{
		Goal: "扩展 antiMove 接口",
		Text: "需要你确认的 3 个点：1. 语义？2. 范围？3. 命名？确认后我就开始写代码。",
	})
	prompt := m.last.Messages[0].Content
	// The prompt must tell the judge that a question previewing a planned
	// action is still a question (the model must stop and wait).
	if !strings.Contains(prompt, "预告") {
		t.Errorf("expected prompt to cover question-with-planned-action (keyword 预告), got:\n%s", prompt)
	}
}

// TestVerdictString verifies TextVerdict.String labels.
func TestVerdictString(t *testing.T) {
	if VerdictIntermediate.String() != "intermediate" {
		t.Errorf("VerdictIntermediate.String() = %q", VerdictIntermediate.String())
	}
	if VerdictConclusion.String() != "conclusion" {
		t.Errorf("VerdictConclusion.String() = %q", VerdictConclusion.String())
	}
	if VerdictQuestion.String() != "question" {
		t.Errorf("VerdictQuestion.String() = %q", VerdictQuestion.String())
	}
}
