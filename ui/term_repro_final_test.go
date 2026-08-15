package ui

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/deepact/deepact/engine"
)

// ===== 终端模拟器：解析 ANSI 控制序列，按指定宽度表写屏 =====
type tm struct {
	w, h  int
	cells [][]rune
	row   int
	col   int
	wf    func(rune) int // 终端宽度表
}

func newTm(w, h int, wf func(rune) int) *tm {
	c := make([][]rune, h)
	for i := range c {
		c[i] = make([]rune, w)
		for j := range c[i] {
			c[i][j] = ' '
		}
	}
	return &tm{w: w, h: h, cells: c, wf: wf}
}

func (t *tm) write(s string) {
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			if i+1 < len(s) && s[i+1] == '[' {
				j := i + 2
				for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
					j++
				}
				if j < len(s) {
					seq := s[i+2 : j]
					cmd := s[j]
					var a, b int
					fmt.Sscanf(seq, "%d;%d", &a, &b)
					switch cmd {
					case 'H':
						if a == 0 {
							a = 1
						}
						if b == 0 {
							b = 1
						}
						t.row = a - 1
						t.col = b - 1
						if t.row < 0 {
							t.row = 0
						}
						if t.col < 0 {
							t.col = 0
						}
					case 'A':
						if a == 0 {
							a = 1
						}
						t.row -= a
						if t.row < 0 {
							t.row = 0
						}
					case 'G':
						if a == 0 {
							a = 1
						}
						t.col = a - 1
					case 'D':
						if a == 0 {
							a = 1
						}
						t.col -= a
						if t.col < 0 {
							t.col = 0
						}
					case 'K':
						for c := t.col; c < t.w; c++ {
							t.cells[t.row][c] = ' '
						}
					case 'J':
						for rr := t.row; rr < t.h; rr++ {
							for cc := 0; cc < t.w; cc++ {
								t.cells[rr][cc] = ' '
							}
						}
					}
					i = j + 1
					continue
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch r {
		case '\r':
			t.col = 0
			continue
		case '\n':
			if t.row < t.h-1 {
				t.row++
			}
			t.col = 0
			continue
		}
		w := t.wf(r)
		if t.col+w > t.w { // autowrap
			if t.row < t.h-1 {
				t.row++
			}
			t.col = 0
		}
		if t.row >= 0 && t.row < t.h && t.col < t.w {
			t.cells[t.row][t.col] = r
			if w == 2 && t.col+1 < t.w {
				t.cells[t.row][t.col+1] = 0
			}
		}
		t.col += w
	}
}

func (t *tm) dump() []string {
	out := make([]string, 0, t.h)
	for _, row := range t.cells {
		var sb strings.Builder
		for _, r := range row {
			if r == 0 {
				continue
			}
			sb.WriteRune(r)
		}
		out = append(out, strings.TrimRight(sb.String(), " "))
	}
	return out
}

// ===== 复刻 fork standardRenderer.flush（alt screen 模式）=====
type rn struct {
	lastRenderedLines []string
	linesRendered     int
	width             int
}

func (r *rn) flush(newLines []string) string {
	var buf strings.Builder
	buf.WriteString(ansi.CursorHomePosition)
	for i := 0; i < len(newLines); i++ {
		canSkip := len(r.lastRenderedLines) > i && r.lastRenderedLines[i] == newLines[i] && !hasWide(newLines[i])
		if canSkip {
			if i < len(newLines)-1 {
				buf.WriteByte('\n')
			}
			continue
		}
		if i == 0 && len(r.lastRenderedLines) == 0 {
			buf.WriteByte('\r')
		}
		line := newLines[i]
		if r.width > 0 {
			line = ansi.Truncate(line, r.width, "")
		}
		if ansi.StringWidth(line) < r.width {
			line += ansi.EraseLineRight
		}
		buf.WriteString(line)
		if i < len(newLines)-1 {
			buf.WriteString("\r\n")
		}
	}
	if r.linesRendered > len(newLines) {
		buf.WriteString(ansi.EraseScreenBelow)
	}
	r.lastRenderedLines = newLines
	r.linesRendered = len(newLines)
	return buf.String()
}

func hasWide(s string) bool {
	for _, r := range s {
		if runewidth.RuneWidth(r) > 1 {
			return true
		}
	}
	return false
}

// ===== 宽度表 =====
// 表P: 项目 displayWidth 语义（Box=1, ambiguous=2）
// 表I: iTerm2 默认（Box=1, ambiguous=1, CJK=2）
func wfProj(r rune) int {
	if r < 128 {
		return 1
	}
	if r >= 0x2500 && r <= 0x259F {
		return 1
	}
	if r == '↑' || r == '↓' || r == '│' || r == '⌥' || r == '↩' {
		return 1
	}
	if r >= 0x2E80 {
		return 2
	}
	return 1
}

// 模拟完整 View() 行：body + footer（复用真实 Model）
func fullViewLines(narration string, termW, termH int) []string {
	m := NewModel(nil, engine.PricingConfig{})
	m.ready = true
	m.state = stateRunning
	m.width = termW
	m.height = termH
	m.messages = []DisplayMessage{
		{Role: "user", Content: "对比 pi 与 Reasonix"},
		{Role: "toolsummary", Content: "● 1 tools executed, 0 files modified\n  [<>] agent/coordinator.go (全文)"},
	}
	m.narration = narration
	m.flushNarration()
	return strings.Split(m.View(), "\n")
}

func TestTermReproFinal(t *testing.T) {
	termW := 80
	termH := 24

	// 帧序列：流式增长（content_delta 逐段）
	steps := []string{
		"",
		"分析",
		"分析结论",
		"分析结论：Deep",
		"分析结论：DeepSeek",
		"分析结论：DeepSeek-Reasonix",
		"分析结论：DeepSeek-Reasonix 有**完整",
		"分析结论：DeepSeek-Reasonix 有**完整的子代理/多 agent 实现**（但没有名为 \"team\" 的模式）",
		"分析结论：DeepSeek-Reasonix 有**完整的子代理/多 agent 实现**（但没有名为 \"team\" 的模式）\n\n与 pi 的\"示例扩展\"定位不同，Reasonix 把多 agent 能力内置进 Go 二进制，共有机制三层：",
	}

	widthTables := map[string]func(rune) int{"proj(Box=1,amb=2)": wfProj}
	for name, wf := range widthTables {
		term := newTm(termW, termH, wf)
		ren := &rn{width: termW}
		// 全量重绘模式：模拟 repaintCmd（每帧清 lastRenderedLines）
		repaintMode := true
		for _, s := range steps {
			lines := fullViewLines(s, termW, termH)
			if repaintMode {
				ren.lastRenderedLines = nil
				ren.linesRendered = 0
			}
			term.write(ren.flush(lines))
		}
		screen := term.dump()
		fmt.Printf("======== 宽度表 %s：最终屏幕（全量重绘）========\n", name)
		for i, l := range screen {
			fmt.Printf("[%02d] %q\n", i, l)
		}
		joined := strings.Join(screen, "\n")
		for _, pat := range []string{"Deep：Seek", "Reason ix", "内置 Go进", "实现 **", "●"} {
			if strings.Contains(joined, pat) {
				fmt.Printf(">>> 含特征 %q\n", pat)
			}
		}
	}
}
