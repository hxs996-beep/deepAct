package ui

import (
	"strings"
	"testing"
)

// TestNarrationStreamingOrder guards the TEXT LAYER of streaming narration:
// it simulates content_delta arriving incrementally and renders the FULL
// View() every tick, asserting that the pure-text output preserves character
// order — "昵称(邮箱" never becomes "昵(称邮箱", "Email1DisplayName" never
// becomes "EmailDisplay1Name".
//
// Scope note: this verifies the View() string pipeline (wrapText, padding,
// truncation). The originally reported visual garbling occurs on the
// TERMINAL layer (Bubble Tea incremental redraw + CJK wide-character cursor
// drift), which cannot be reproduced in a pure-Go unit test — it needs a real
// PTY. Keeping streaming output ANSI-free (see renderStreaming) reduces that
// drift by making re-rendered lines plain and byte-identical when unchanged.
func TestNarrationStreamingOrder(t *testing.T) {
	full := "格式grep已锁定两处完全匹配的拼接（点ContactConverter.java442、560，均生成Email1DisplayName。其中昵称(邮箱)应该是这样，但是我居然看到打印"
	widths := []int{30, 40, 60, 80, 100, 120, 160, 200}
	failures := 0
	for _, w := range widths {
		m := &Model{
			state:    stateRunning,
			ready:    true,
			width:    w,
			height:   30,
			inputBuf: NewInputBuffer(),
			msgCache: &messageRenderCache{},
		}
		for i := 1; i <= len(full); i++ {
			m.narration = full[:i]
			m.flushNarration()
			out := m.View()
			plain := stripAnsi(out)
			joined := compactText(plain)
			wantSub := compactText(full[:i])
			if !strings.Contains(joined, wantSub) {
				if failures < 12 {
					t.Errorf("width=%d prefix_len=%d\n  joined=%q\n  want  =%q", w, i, truncateStr(joined, 120), truncateStr(wantSub, 120))
				}
				failures++
				if failures == 1 {
					for _, l := range strings.Split(plain, "\n") {
						if strings.Contains(l, "昵") || strings.Contains(l, "Email") || strings.Contains(l, "442") {
							t.Logf("  line: %q", truncateStr(l, 120))
						}
					}
				}
			}
		}
	}
	if failures > 0 {
		t.Fatalf("streaming reorder reproduced: %d failing frames across %d widths", failures, len(widths))
	}
}

func compactText(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
