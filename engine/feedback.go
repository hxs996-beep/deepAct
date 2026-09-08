package engine

import (
	"regexp"
	"strings"
)

// buDuiRe 匹配"不对"的负面用法：独立成句或带语气/标点。
// 避免误伤条件句"如果不对就跳过"（"不对就"无标点且不以"不对"开头）。
var buDuiRe = regexp.MustCompile(`(?m)^\s*不对[，。！？!?]|(?m)^\s*不对吧|(?m)^\s*不对$`)

// jiuZheRe 匹配"就这"的负面用法：后接标点/语气词或行尾。
// 避免误伤"就这个接口为什么报错"（"就这"后接"个"不是负面）。
var jiuZheRe = regexp.MustCompile(`就这[？！!?。，]|就这$`)

// negativePatterns 是中文负面情绪关键词（失望/反驳/质疑/停止重做）。
var negativePatterns = []string{
	// 失望/不满
	"错了", "没用", "白做", "白费", "浪费", "不行", "太差", "差劲",
	"垃圾", "什么玩意", "失望", "离谱", "莫名其妙", "一塌糊涂", "糊弄",
	// 反驳/纠正方向
	"不是这样", "不是我要的", "我让你", "你理解错了", "搞错了", "想错了",
	"方向不对", "走偏了", "跑偏了", "没按我说的", "说反了",
	// 质疑/批评
	"你怎么搞的", "就这水平", "这也算", "什么逻辑", "越做越差",
	"还不如之前", "能力不行",
	// 停止/重做
	"重新来", "重做", "再来一遍", "推倒重来", "停下", "停一下", "别做了",
	"别改了", "撤销", "回退", "恢复原样",
}

// negativeEnglishPatterns 是英文负面关键词（参考 claude-code 的 userPromptKeywords）。
var negativeEnglishPatterns = []string{
	"this sucks", "not what i asked", "that's wrong", "try again",
	"undo that", "why did you", "wrong direction",
}

// detectNegativeFeedback 检测用户消息是否为负面情绪反馈。
// 纯关键词匹配，零 LLM 成本，每轮用户消息调用一次。
func detectNegativeFeedback(userMsg string) bool {
	msg := strings.TrimSpace(userMsg)
	if msg == "" {
		return false
	}
	if buDuiRe.MatchString(msg) || jiuZheRe.MatchString(msg) {
		return true
	}
	lower := strings.ToLower(msg)
	for _, p := range negativeEnglishPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	for _, p := range negativePatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}
