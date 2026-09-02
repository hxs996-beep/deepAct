package tea

import "testing"

// lineHasWideRune 决定增量 diff 哪些行强制重绘。它必须与 DeepAct ui 侧的
// 唯一宽度口径（termWidthCond，EastAsianWidth=true）同源：ambiguous 宽度
// 字符（— • → ← “ ” ·）在 iTerm2 CJK 环境渲染为 2 列，若按默认 runewidth
// 条件计 1 列则这些行会被 skip，漂移后出现重复行/串行。
// Box Drawing / Block Elements（U+2500-0x259F）与 ui 的 runeVisualWidth
// 保持同步：终端按 1 列渲染，不视为宽行。
func TestLineHasWideRune_AmbiguousIsWide(t *testing.T) {
	ambiguous := []rune{'—', '•', '→', '←', '“', '”', '·'}
	for _, r := range ambiguous {
		if !lineHasWideRune(string(r)) {
			t.Errorf("ambiguous rune %q (U+%04X) should be treated as wide", r, r)
		}
	}

	if !lineHasWideRune("中文") {
		t.Error("CJK runes should be treated as wide")
	}

	if lineHasWideRune("█╔═║│▍") {
		t.Error("box drawing runes should stay 1 column (pinned by ui runeVisualWidth)")
	}

	if lineHasWideRune("plain ascii 123") {
		t.Error("ascii should not be treated as wide")
	}
}
