package engine

import "testing"

func TestDetectNegativeFeedback_Hits(t *testing.T) {
	positives := []string{
		// 失望/不满
		"不对，这方向有问题",
		"这个方案没用",
		"白做了一下午",
		"太差了",
		"什么垃圾代码",
		// 反驳/纠正方向
		"不是这样，我让你改这里",
		"你理解错了",
		"方向不对",
		"跑偏了",
		// 质疑/批评
		"你怎么搞的",
		"就这水平？",
		"越做越差",
		// 停止/重做
		"重新来",
		"推倒重来",
		"停下，别做了",
		"撤销刚才的修改",
		// 英文
		"this sucks",
		"not what I asked",
		"that's wrong",
		"try again",
		"why did you change it",
	}
	for _, msg := range positives {
		if !detectNegativeFeedback(msg) {
			t.Errorf("detectNegativeFeedback(%q) = false, want true", msg)
		}
	}
}

func TestDetectNegativeFeedback_Misses(t *testing.T) {
	negatives := []string{
		"停车场怎么走",
		"如果不对就跳过这个",
		"确认",
		"继续",
		"/confirm 1",
		"/clear",
		"帮我加个按钮",
		"这个接口为什么报错",
		"可以",
	}
	for _, msg := range negatives {
		if detectNegativeFeedback(msg) {
			t.Errorf("detectNegativeFeedback(%q) = true, want false", msg)
		}
	}
}
