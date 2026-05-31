package policy

import (
	"context"
	"encoding/json"
	"unicode"

	"github.com/deepact/deepact/engine"
)

type AmbiguityResult = engine.AmbiguityResult

type AmbiguityDetector struct {
	Threshold float64
}

func NewAmbiguityDetector(threshold float64) *AmbiguityDetector {
	return &AmbiguityDetector{Threshold: threshold}
}

// AnalyzeWithLLM uses an LLM call to score request ambiguity instead of keyword matching.
// Returns structured ambiguity assessment with score and clarifying questions.
func (d *AmbiguityDetector) AnalyzeWithLLM(ctx context.Context, client engine.ModelClient, modelName, userMsg string) engine.AmbiguityResult {
	if len(userMsg) < 5 {
		return engine.AmbiguityResult{Score: 0.0}
	}

	zh := isChinese(userMsg)
	prompt := `Score how ambiguous/vague this request is (0.0 = perfectly clear, 1.0 = extremely vague).

A request is ambiguous if:
- It uses open-ended verbs without specifics ("improve", "optimize", "refactor", "make better", "优化", "改进")
- It has multiple possible interpretations ("maybe", "or something", "whatever", "或者", "大概")
- The scope or desired outcome is unclear

A request is CLEAR if:
- It states what to do and the expected result
- File paths, function names, or concrete changes are mentioned
- The goal is specific and actionable

Output JSON: {"score": <0.0-1.0>, "questions": ["clarifying question if score > 0.4"]}`
	if zh {
		prompt = `评估此请求的模糊程度（0.0 = 非常清晰，1.0 = 极其模糊）。

模糊的请求特征：
- 使用了开放式的动词但没有具体说明（"优化"、"改进"、"重构"）
- 有多种可能的理解方向（"或者"、"大概"、"随便"）
- 范围或期望结果不明确

清晰的请求特征：
- 说明了具体要做什么以及预期结果
- 提到了文件路径、函数名或具体的改动
- 目标是具体且可执行的

输出 JSON: {"score": <0.0-1.0>, "questions": ["澄清问题（如果 score > 0.4）"]}`
	}

	req := engine.ModelRequest{
		Model:       modelName,
		Messages:    []engine.ModelMessage{{Role: "system", Content: prompt}, {Role: "user", Content: userMsg}},
		MaxTokens:   256,
		Temperature: 0.0,
		JsonMode:    true,
	}

	resp, err := client.Complete(ctx, req)
	if err != nil {
		return engine.AmbiguityResult{Score: 0.0}
	}

	var result struct {
		Score     float64  `json:"score"`
		Questions []string `json:"questions"`
	}
	if err := json.Unmarshal([]byte(resp.Message.Content), &result); err != nil {
		return engine.AmbiguityResult{Score: 0.0}
	}

	return engine.AmbiguityResult{
		Score:     result.Score,
		Questions: result.Questions,
	}
}

func (d *AmbiguityDetector) ShouldBlock(result engine.AmbiguityResult) bool {
	return result.Score >= d.Threshold
}

func isChinese(msg string) bool {
	for _, r := range msg {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
