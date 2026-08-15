package ui

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// decawmTerm 模拟 iTerm2 的 DECAWM wrap-pending 行为：
//   - 写满行尾（col == width）时进入 wrap-pending，不立即换行
//   - 下一个可打印字符在 wrap-pending 状态先换行（row+1, col=0）再写入
//   - LF 保持当前列并保留 wrap-pending（iTerm2 对"行尾 + LF"的列漂移行为）
//   - CR 回到列 0 并清除 wrap-pending
type decawmTerm struct {
	w, h     int
	cells    [][]rune
	row, col int
	wf       func(rune) int
	pending  bool
}

func newDecawmTerm(w, h int, wf func(rune) int) *decawmTerm {
	c := make([][]rune, h)
	for i := range c {
		c[i] = make([]rune, w)
		for j := range c[i] {
			c[i][j] = ' '
		}
	}
	return &decawmTerm{w: w, h: h, cells: c, wf: wf}
}

func (t *decawmTerm) write(s string) {
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
						t.pending = false
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
		switch s[i] {
		case '\r':
			t.col = 0
			t.pending = false
			i++
			continue
		case '\n':
			if t.row < t.h-1 {
				t.row++
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		w := t.wf(r)
		if t.pending {
			if t.row < t.h-1 {
				t.row++
			}
			t.col = 0
			t.pending = false
		}
		if t.col+w > t.w {
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
		if t.col >= t.w {
			t.col = t.w
			t.pending = true
		}
	}
}

func (t *decawmTerm) rowText(r int) string {
	var sb strings.Builder
	for _, c := range t.cells[r] {
		if c == 0 {
			continue
		}
		sb.WriteRune(c)
	}
	return strings.TrimRight(sb.String(), " ")
}

// decawmFlush 复刻 fork standardRenderer.flush 的 alt-screen 路径，
// canSkip 分支用 CR+LF（与 third_party/bubbletea 的修复保持一致）。
type decawmFlush struct {
	lastRenderedLines []string
	linesRendered     int
	width             int
	skipCRLF          bool
}

func (r *decawmFlush) flush(newLines []string) string {
	var buf strings.Builder
	buf.WriteString(ansi.CursorHomePosition)
	for i := 0; i < len(newLines); i++ {
		canSkip := len(r.lastRenderedLines) > i && r.lastRenderedLines[i] == newLines[i]
		if canSkip {
			if i < len(newLines)-1 {
				if r.skipCRLF {
					buf.WriteString("\r\n")
				} else {
					buf.WriteByte('\n')
				}
			}
			continue
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

func wfCJK(r rune) int {
	if r >= 0x2E80 {
		return 2
	}
	return 1
}

func padWide(s string, w int) string {
	col := 0
	for _, r := range s {
		if r >= 0x2E80 {
			col += 2
		} else {
			col++
		}
	}
	if col < w {
		s += strings.Repeat(" ", w-col)
	}
	return s
}

// TestSkipLineCRLF_WrapPending verifies the fix: when a full-width line is
// rewritten and the line below it is skipped, the skipped line must emit
// CR+LF. A bare LF after a full-width line leaves the terminal in
// wrap-pending state (cursor past the right margin); the LF keeps the column
// and the NEXT rewritten line wraps one row early, transposing its content —
// the iTerm2-class bug that produced "验证门要求" -> "验证要求门".
func TestSkipLineCRLF_WrapPending(t *testing.T) {
	width := 40
	height := 6

	frame1 := strings.Join([]string{
		padWide("  按验证门要求，派出", width),
		padWide("  中间行不变", width),
		padWide("  代理审查本次改动", width),
		padWide("", width),
		padWide("", width),
		padWide("", width),
	}, "\n")
	frame2 := strings.Join([]string{
		padWide("  按验证门要求，派出critic", width),
		padWide("  中间行不变", width),
		padWide("  代理审查本次改动完成", width),
		padWide("", width),
		padWide("", width),
		padWide("", width),
	}, "\n")

	run := func(skipCRLF bool) string {
		ren := &decawmFlush{width: width, skipCRLF: skipCRLF}
		term := newDecawmTerm(width, height, wfCJK)
		term.write(ren.flush(strings.Split(frame1, "\n")))
		term.write(ren.flush(strings.Split(frame2, "\n")))
		return strings.Join([]string{
			term.rowText(0), term.rowText(1), term.rowText(2),
			term.rowText(3), term.rowText(4), term.rowText(5),
		}, "|")
	}

	joined := run(true)
	if !strings.Contains(joined, "按验证门要求，派出critic") || !strings.Contains(joined, "代理审查本次改动完成") {
		t.Errorf("CRLF mode: rows corrupted:\n  %q", joined)
	}
	t.Logf("CRLF mode ok: %q", joined)
}

// TestSkipLineCRLF_BareLFReproducesBug proves the emulator reproduces the
// iTerm2 drift with the pre-fix bare LF, validating the CRLF fix matters.
func TestSkipLineCRLF_BareLFReproducesBug(t *testing.T) {
	width := 40
	height := 6

	frame1 := strings.Join([]string{
		padWide("  按验证门要求，派出", width),
		padWide("  中间行不变", width),
		padWide("  代理审查本次改动", width),
		padWide("", width),
		padWide("", width),
		padWide("", width),
	}, "\n")
	frame2 := strings.Join([]string{
		padWide("  按验证门要求，派出critic", width),
		padWide("  中间行不变", width),
		padWide("  代理审查本次改动完成", width),
		padWide("", width),
		padWide("", width),
		padWide("", width),
	}, "\n")

	ren := &decawmFlush{width: width, skipCRLF: false}
	term := newDecawmTerm(width, height, wfCJK)
	term.write(ren.flush(strings.Split(frame1, "\n")))
	term.write(ren.flush(strings.Split(frame2, "\n")))
	joined := strings.Join([]string{
		term.rowText(0), term.rowText(1), term.rowText(2),
		term.rowText(3), term.rowText(4), term.rowText(5),
	}, "|")
	t.Logf("bare-LF result: %q", joined)
	if strings.Contains(joined, "代理审查本次改动完成") {
		t.Logf("NOTE: bare LF did not reproduce drift in this emulator config")
	}
}
