package engine

import (
	"encoding/json"
)

// DefaultResumeBudget 是 /resume 恢复历史的最大 token 预算，
// 对齐 compressor.go 的 tailBudget=16384（DeepSeek 缓存甜点区）。
const DefaultResumeBudget = 16384

// tokenCount 粗略估算文本 token 数（约 4 字符/token）。
func tokenCount(s string) int {
	return len([]rune(s))/4 + 1
}

// RebuildHistory 从会话事件重建可重放的对话历史：
//  1. 过滤 message 事件
//  2. 跳过 tool 消息、剥离 ToolCalls（规避 assistant(tool_calls)→tool 的 API 契约）
//  3. 从尾部向前按 budget 累加 token，超预算时切割到最近的 user 消息边界，
//     保证恢复起点是完整用户提问
func RebuildHistory(events []Event, budget int) []Message {
	var msgs []Message
	for _, ev := range events {
		if ev.Type != EventTypeMessage {
			continue
		}
		var m Message
		if err := json.Unmarshal(ev.Payload, &m); err != nil {
			continue
		}
		if m.Role == "tool" {
			continue
		}
		m.ToolCalls = nil
		msgs = append(msgs, m)
	}
	if budget <= 0 || len(msgs) == 0 {
		return msgs
	}

	// 从尾部向前累加 token，记录第一个使 total 超预算的索引。
	total := 0
	overflow := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		total += tokenCount(msgs[i].Content)
		if total > budget {
			overflow = i
			break
		}
	}
	if overflow < 0 {
		return msgs // 全部在预算内
	}

	// 窗口从越界点之后开始，向后找最近的 user 消息作为恢复起点。
	// 若剩余窗口内没有 user 消息（例如预算只够一条孤立 assistant），
	// 则保持越界点之后的位置，不引入超预算的越界消息。
	start := overflow + 1
	for j := start; j < len(msgs); j++ {
		if msgs[j].Role == "user" {
			start = j
			break
		}
	}
	return msgs[start:]
}
