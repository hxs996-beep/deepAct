package ui

import (
	"strings"
	"testing"

	"github.com/deepact/deepact/engine"
)

// A 的目标行为：方案场景下 narration 缓冲是 Summary 的【超集】——
// 被分析 gate 拦截的 turn（有流式文本、无工具执行，不触发 tool_start 清空）
// 残留文本 + 报告 turn 全文累积到同一缓冲。
// finishStreaming 去重目前用 normalizeForCompare 精确相等：超集 ≠ Summary →
// 不判重复 → 纯文本 narration（format 前）+ formatted Summary（format 后）两笔。
// A 实施后：narration 包含 Summary 即判重复 → 移除纯文本 narration（含残留），
// 报告文本只出现一次（formatted Summary）。
func TestReproOptionsDup_NarrationSuperSet(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateReady,
		msgCache: &messageRenderCache{},
	}
	m.runStartMsgIdx = 0
	m.messages = []DisplayMessage{{Role: "user", Content: "优化方案显示"}}

	// 被 gate 拦截 turn 的残留文本 + 报告 turn 全文（引擎 content_delta 累积的形态）
	residual := "修改代码"
	report := "完整报告：根因是 narration 超集。\n\n方案A：改为前缀匹配。\n方案B：改引擎 Summary 源。"
	m.narration = residual + "\n\n" + report

	m.finishStreaming(EngineResponseMsg{
		Response: &engine.EngineResponse{
			Summary: report,
			Options: []string{"改为前缀匹配", "改引擎 Summary 源", "输入你的意见"},
		},
	})

	// 期望：报告文本只出现一次（formatted assistant Summary）
	count := 0
	for _, msg := range m.messages {
		if (msg.Role == "narration" || msg.Role == "assistant") && strings.Contains(msg.Content, "完整报告") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("报告文本出现 %d 次（期望 1 次）——两笔 bug 复现: %+v", count, m.messages)
	}
}
