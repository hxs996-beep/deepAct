package engine

import (
	"context"
	"strings"
	"testing"
)

// TestSubAgent_TextOnly_NoClassifierProbe 锁定 C5 的确定性完成语义：
// 子代理的文本回合完成判定绝不依赖 LLM classifier 探针——即使 classifier
// 会返回"是结论"，文本回合也只走确定性 3-strike（nudge → StalledNarration）。
// 当前代码在非结构化 sub-agent 上仍会探测 classifier（classifierCalls>0），
// 所以这是红灯。
func TestSubAgent_TextOnly_NoClassifierProbe(t *testing.T) {
	model := &stubSeqModel{
		responses: []ModelResponse{
			{Message: ModelMessage{Role: "assistant", Content: "完成了。"}, FinishReason: "stop"},
		},
		classifierResp: `{"conclusion": true}`, // classifier 若被探测会误判为结论
	}

	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}
	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "g", MaxIterations: 8,
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if model.classifierCalls != 0 {
		t.Errorf("expected NO classifier probe (deterministic completion), got %d", model.classifierCalls)
	}
	if result.FinishReason != HandoffReasonStalledNarration {
		t.Errorf("expected deterministic FinishReason=%q (3-strike), got %q", HandoffReasonStalledNarration, result.FinishReason)
	}
	if !strings.Contains(result.Summary, "完成了") {
		t.Errorf("expected partial text preserved, got %q", result.Summary)
	}
}
