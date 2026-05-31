package ui

import (
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// isTerminalEscapeResidue detects rune sequences that are the visible residue
// of terminal escape sequences that leaked past Bubble Tea's key interception.
// We only catch patterns that are DETERMINISTICALLY escape residue and can
// NEVER appear in legitimate keyboard input.
//
// The key insight from DeepSeek-TUI (crossterm-based): instead of trying to
// catch every possible leak pattern with broad regexes (which also catch
// legitimate text like "/feat" or "dead/beef"), we keep filtering minimal:
//   - OSC sequences starting with ']' + number + ';' (color query responses)
//   - CSI private markers '[<', '[?', '[=' which are ALWAYS escape sequence starts
//   - SGR mouse event continuations: digits;digits;digitsM/m (leaked after [<)
//   - Known color response keywords "rgb:" / "rgba:"
//   - Pure C0/C1 control chars are caught by isLikelyControlOrphan
//
// Everything else is legitimate input. If an occasional escape byte leaks into
// the input, the user backspaces it — far better than silently eating typed chars.
func isTerminalEscapeResidue(runes []rune) bool {
	if len(runes) == 0 {
		return false
	}

	// ---- OSC (Operating System Command) sequences ----
	// Format: ESC ] N ; params ST
	// When ESC (0x1B) is consumed by Bubble Tea, what remains is "]N;params".
	// e.g. "]11;rgb:fae0/fae0/fae0" — OSC 11 color query response.
	if runes[0] == ']' && len(runes) > 2 {
		idx := 1
		for idx < len(runes) && runes[idx] >= '0' && runes[idx] <= '9' {
			idx++
		}
		if idx > 1 && idx < len(runes) && runes[idx] == ';' {
			return true
		}
	}

	// ---- CSI private prefix: [<, [?, [= ----
	// These are always the start of a leaked escape sequence (SGR mouse,
	// DEC private mode). NEVER appear in legitimate keyboard input.
	// > = Secondary DA, ! = Soft reset, * = DEC private (some terminals)
	if len(runes) >= 2 && runes[0] == '[' && (runes[1] == '<' || runes[1] == '?' || runes[1] == '=' || runes[1] == '>' || runes[1] == '!' || runes[1] == '*') {
		return true
	}

	// ---- CPR (Cursor Position Report): [<row>;<col>R ----
	// Terminal response to \x1b[6n (DSR). The ESC byte is consumed by the
	// terminal layer, leaving [<row>;<col>R as visible residue.
	// Pattern: [, digits, ;, digits, R
	if len(runes) >= 5 && runes[0] == '[' && runes[len(runes)-1] == 'R' {
		semiIdx := -1
		for i := 1; i < len(runes)-1; i++ {
			if runes[i] == ';' {
				if semiIdx != -1 { // multiple semicolons — not CPR
					semiIdx = -1
					break
				}
				semiIdx = i
			} else if runes[i] < '0' || runes[i] > '9' {
				semiIdx = -1
				break
			}
		}
		if semiIdx > 1 && semiIdx < len(runes)-2 { // digits on both sides
			return true
		}
	}

	// ---- SGR mouse event tail: digits;digits;digitsM/m ----
	// When the [< prefix was filtered in a prior message, the remaining tail
	// (e.g. "64;24;31M") arrives as a separate message. This pattern is
	// extremely specific and never appears in legitimate input.
	if len(runes) >= 5 {
		// Pattern: 1+ digits, ';', 1+ digits, ';', 1+ digits, 'M' or 'm'
		last := len(runes) - 1
		if (runes[last] == 'M' || runes[last] == 'm') && runes[last-1] >= '0' && runes[last-1] <= '9' {
			// Walk backwards: digits, then ';', then digits, then ';', then digits
			j := last - 2
			if j >= 0 && runes[j] == ';' {
				j--
				if j >= 0 && runes[j] >= '0' && runes[j] <= '9' {
					for j >= 0 && runes[j] >= '0' && runes[j] <= '9' {
						j--
					}
					if j >= 0 && runes[j] == ';' {
						j--
						if j >= 0 && runes[j] >= '0' && runes[j] <= '9' {
							// All the way back to start or first non-digit
							for j >= 0 && runes[j] >= '0' && runes[j] <= '9' {
								j--
							}
							if j < 0 {
								// Entire slice matches digits;digits;digitsM/m
								return true
							}
						}
					}
				}
			}
		}
	}

	// ---- Color response keywords ----
	if strings.Contains(string(runes), "rgb:") || strings.Contains(string(runes), "rgba:") {
		return true
	}

	// ---- Hex color value (leaked without 'rgb:' prefix) ----
	// Terminal color query responses like \x1b]11;rgb:fae0/fae0/fae0\x07 can arrive
	// as separate KeyRunes events when the kernel PTY buffer splits the response.
	// The 'rgb:' prefix is caught above, but the hex value "fae0/fae0/fae0" may
	// arrive in its own batch and needs its own check.
	// Pattern: exactly 3 or 4 hex groups (2-4 hex digits each) separated by '/'.
	if isColorHexValue(string(runes)) {
		return true
	}

	return false
}

// isColorHexValue checks if s looks like a terminal color hex value (e.g. "fae0/fae0/fae0").
// This pattern is extremely specific and NEVER appears in legitimate keyboard input.
func isColorHexValue(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) != 3 && len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if len(p) < 2 || len(p) > 4 {
			return false
		}
		for _, c := range p {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// isLikelyControlOrphan returns true for individual runes that are almost
// certainly orphaned control-sequence bytes rather than intentional input.
// We only filter C0 control chars (0x00-0x1F) and DEL (0x7F). These are the
// only bytes that are NEVER typed as input in modern terminal emulators.
//
// We intentionally DO NOT filter C1 controls (0x80-0x9F) or Unicode categories
// Co/Cn — these can appear in legitimate input on non-US keyboard layouts and
// IME input methods, and modern terminals (iTerm2, Terminal.app, Warp, Kitty)
// never emit them as escape residue.
func isLikelyControlOrphan(r rune) bool {
	return r < 0x20 || r == 0x7F
}

// filterRunes applies both batch-level escape residue detection and
// per-rune control-character filtering to a slice of runes. Returns the
// filtered runes (safe to insert) or nil if the entire batch is garbage.
// This is the unified filtering entry point used by both InputBuffer.HandleKey
// and model-level key handling.
func filterRunes(runes []rune) []rune {
	if isTerminalEscapeResidue(runes) {
		return nil
	}
	filtered := make([]rune, 0, len(runes))
	for _, r := range runes {
		if !isLikelyControlOrphan(r) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// InputAction describes what the application should do in response to input.
type InputAction int

const (
	ActionNone InputAction = iota
	ActionQuit
	ActionSubmit
	ActionNewline
	ActionRuneInserted
	ActionBackspace
	ActionDelete
	ActionCursorLeft
	ActionCursorRight
	ActionCursorHome
	ActionCursorEnd
	ActionCopySelected
)

// InputBuffer manages the text input state: text buffer, cursor, and selection.
type InputBuffer struct {
	text   []rune
	cursor int

	// Selection / drag state
	selStart int // -1 = no active selection
	selEnd   int
	dragging bool
}

func NewInputBuffer() *InputBuffer {
	return &InputBuffer{selStart: -1}
}

func (ib *InputBuffer) Value() string {
	return string(ib.text)
}

func (ib *InputBuffer) SetValue(s string) {
	ib.text = []rune(s)
	ib.cursor = len(ib.text)
	ib.clearSelection()
}

func (ib *InputBuffer) Cursor() int {
	return ib.cursor
}

func (ib *InputBuffer) Len() int {
	return len(ib.text)
}

func (ib *InputBuffer) clearSelection() {
	ib.selStart = -1
	ib.selEnd = -1
}

func (ib *InputBuffer) clampCursor() {
	if ib.cursor < 0 {
		ib.cursor = 0
	}
	if ib.cursor > len(ib.text) {
		ib.cursor = len(ib.text)
	}
}

func (ib *InputBuffer) insertAtCursor(r rune) {
	ib.text = append(ib.text[:ib.cursor], append([]rune{r}, ib.text[ib.cursor:]...)...)
	ib.cursor++
}

func (ib *InputBuffer) insertRunes(runes []rune) {
	for _, r := range runes {
		ib.insertAtCursor(r)
	}
}

// HandleKey processes a keyboard event and returns the action to take.
// The buffer is mutated inline for insert/delete/cursor operations.
func (ib *InputBuffer) HandleKey(msg tea.KeyMsg) InputAction {
	switch {
	case msg.Type == tea.KeyEnter && !msg.Alt:
		// Plain Enter -> submit
		ib.clearSelection()
		return ActionSubmit

	case msg.Type == tea.KeyEnter && msg.Alt:
		// Option/Alt+Enter -> newline (Mac Option key = Alt in terminals)
		ib.clearSelection()
		ib.insertAtCursor('\n')
		return ActionNewline

	case msg.Type == tea.KeyBackspace:
		ib.clearSelection()
		if ib.cursor > 0 {
			ib.text = append(ib.text[:ib.cursor-1], ib.text[ib.cursor:]...)
			ib.cursor--
		}
		return ActionBackspace

	case msg.Type == tea.KeyDelete:
		ib.clearSelection()
		if ib.cursor < len(ib.text) {
			ib.text = append(ib.text[:ib.cursor], ib.text[ib.cursor+1:]...)
		}
		return ActionDelete

	case msg.Type == tea.KeyLeft:
		if ib.cursor > 0 {
			ib.cursor--
		}
		return ActionCursorLeft

	case msg.Type == tea.KeyRight:
		if ib.cursor < len(ib.text) {
			ib.cursor++
		}
		return ActionCursorRight

	case msg.Type == tea.KeyHome:
		ib.cursor = 0
		return ActionCursorHome

	case msg.Type == tea.KeyEnd:
		ib.cursor = len(ib.text)
		return ActionCursorEnd

	case msg.Type == tea.KeyRunes:
		ib.clearSelection()
		// Filter out control characters and escape sequences that leak from
		// terminal responses (e.g., OSC 11 color query, SGR mouse events) that
		// Bubble Tea doesn't fully intercept as typed messages.
		runes := msg.Runes
		// Step 1: batch-level check — if the entire rune sequence looks like
		// an escape sequence residue (e.g. "[13;57M" or ";13;57M"), discard the
		// whole batch. These are never legitimate user input.
		if isTerminalEscapeResidue(runes) {
			return ActionNone
		}
		// Step 2: individual character filtering — remove control characters,
		// DEL, C1 controls, and private-use/non-characters.
		filtered := make([]rune, 0, len(runes))
		for _, r := range runes {
			if !isLikelyControlOrphan(r) {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) > 0 {
			ib.insertRunes(filtered)
			return ActionRuneInserted
		}
		return ActionNone

	case msg.Type == tea.KeySpace:
		ib.clearSelection()
		ib.insertAtCursor(' ')
		return ActionRuneInserted
	}

	return ActionNone
}

// HandleMouse handles mouse events for text selection with modifier keys.
// On Mac: Option (Alt) + left drag selects and copies.
// On Win: Shift + left drag selects and copies.
//
// Returns ActionCopySelected + the selected text, or ActionNone + "".
func (ib *InputBuffer) HandleMouse(msg tea.MouseMsg, innerWidth int) (InputAction, string) {
	if msg.Button != tea.MouseButtonLeft {
		return ActionNone, ""
	}

	// Modifier must be held to activate selection mode
	isMod := msg.Alt || msg.Shift

	switch msg.Action {
	case tea.MouseActionPress:
		if isMod {
			pos := mouseToTextPos(msg.X, msg.Y, ib.text, innerWidth)
			ib.selStart = pos
			ib.selEnd = pos
			ib.dragging = true
		} else {
			ib.clearSelection()
			ib.dragging = false
		}
		return ActionNone, ""

	case tea.MouseActionMotion:
		if ib.dragging {
			pos := mouseToTextPos(msg.X, msg.Y, ib.text, innerWidth)
			ib.selEnd = pos
		}
		return ActionNone, ""

	case tea.MouseActionRelease:
		if ib.dragging && ib.selStart >= 0 {
			ib.dragging = false
			start, end := selectionRange(ib.selStart, ib.selEnd)
			if start < end {
				text := string(ib.text[start:end])
				ib.clearSelection()
				return ActionCopySelected, text
			}
			ib.clearSelection()
		}
		return ActionNone, ""
	}

	return ActionNone, ""
}

// selectionRange returns (start, end) where start <= end.
func selectionRange(a, b int) (int, int) {
	if a < 0 {
		return 0, 0
	}
	if a <= b {
		return a, b
	}
	return b, a
}

// mouseToTextPos converts mouse coordinates (relative to the input box area)
// to a position in the text buffer.
//
// It simulates the same wrapping used by renderInputLine + wrapInputText:
//   - "> " prefix on the first line
//   - Each rendered cell maps to one text character
//   - Hard newlines (\n) break the line immediately
func mouseToTextPos(mx, my int, text []rune, innerWidth int) int {
	if len(text) == 0 {
		return 0
	}

	fullLen := len(text) + 2 // "> " prefix

	// Build line-start positions in fullText space ("> " + text)
	var lineStarts []int
	lineStarts = append(lineStarts, 0)

	pos := 0
	for pos < fullLen {
		// Scan ahead up to innerWidth for a \n
		hasNL := false
		end := pos + innerWidth
		if end > fullLen {
			end = fullLen
		}
		for i := pos; i < end; i++ {
			var ch rune
			if i < 2 {
				ch = rune("> "[i])
			} else {
				ch = text[i-2]
			}
			if ch == '\n' {
				lineStarts = append(lineStarts, i+1)
				pos = i + 1
				hasNL = true
				break
			}
		}
		if hasNL {
			continue
		}
		// No \n in this segment, wrap at innerWidth
		pos += innerWidth
		if pos < fullLen {
			lineStarts = append(lineStarts, pos)
		}
	}

	if my < 0 {
		my = 0
	}
	if my >= len(lineStarts) {
		my = len(lineStarts) - 1
	}
	if mx < 0 {
		mx = 0
	}

	// Clamp to line length
	lineStart := lineStarts[my]
	nextStart := fullLen
	if my+1 < len(lineStarts) {
		nextStart = lineStarts[my+1]
	}
	lineLen := nextStart - lineStart
	if mx >= lineLen {
		mx = lineLen
	}

	fullPos := lineStart + mx
	if fullPos > fullLen {
		fullPos = fullLen
	}

	// Convert from fullText position to text buffer position
	bufPos := fullPos - 2 // subtract "> "
	if bufPos < 0 {
		bufPos = 0
	}
	if bufPos > len(text) {
		bufPos = len(text)
	}
	return bufPos
}

// ---------------------------------------------------------------------------
// Platform-specific clipboard copy
// ---------------------------------------------------------------------------

// copyToClipboardMsg is sent when clipboard copy completes.
type copyToClipboardMsg struct{ err error }

// copyToClipboardCmd returns a tea.Cmd that copies text to the system clipboard
// using the platform's native clipboard tool (pbcopy/clip/xclip).
func copyToClipboardCmd(text string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("pbcopy")
		case "windows":
			cmd = exec.Command("clip")
		default:
			// Linux: try xclip first, then wl-copy
			if _, err := exec.LookPath("xclip"); err == nil {
				cmd = exec.Command("xclip", "-selection", "clipboard")
			} else if _, err := exec.LookPath("wl-copy"); err == nil {
				cmd = exec.Command("wl-copy")
			} else {
				return copyToClipboardMsg{err: nil} // silently skip
			}
		}
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			return copyToClipboardMsg{err: err}
		}
		return copyToClipboardMsg{err: nil}
	}
}
