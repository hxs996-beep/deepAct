package ui

import (
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
	// Also catch "]N" or "]NN" (digits-only after ]) which occurs when the PTY
	// splits between the OSC number and the semicolon.
	if runes[0] == ']' && len(runes) >= 2 {
		idx := 1
		for idx < len(runes) && runes[idx] >= '0' && runes[idx] <= '9' {
			idx++
		}
		if idx > 1 {
			// All chars after ']' are digits (split before ';'), or ';' found
			if idx == len(runes) || runes[idx] == ';' {
				return true
			}
		}
	}

	// ---- CSI private prefix: [<, [?, [= ----
	// These are always the start of a leaked escape sequence (SGR mouse,
	// DEC private mode). NEVER appear in legitimate keyboard input.
	// > = Secondary DA, ! = Soft reset, * = DEC private (some terminals)
	if len(runes) >= 2 && runes[0] == '[' && (runes[1] == '<' || runes[1] == '?' || runes[1] == '=' || runes[1] == '>' || runes[1] == '!' || runes[1] == '*') {
		return true
	}

	// ---- CSI '<' split with multi-digit params: <digits;digits;digitsM ----
	// When SGR mouse sequences split at buffer boundaries, the '<' prefix and
	// three semicolon-delimited numbers end with M/m. Use simple string matching
	// since this pattern is extremely specific.
	if len(runes) >= 5 && runes[0] == '<' {
		s := string(runes)
		last := s[len(s)-1]
		if (last == 'M' || last == 'm') && strings.Count(s, ";") == 2 {
			// Split by ';' to check each segment is digits
			parts := strings.Split(s, ";")
			if len(parts) == 3 &&
				parts[0][0] == '<' && isAllDigits(parts[0][1:]) &&
				isAllDigits(parts[1]) &&
				isAllDigits(parts[2][:len(parts[2])-1]) {
				return true
			}
		}
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

	// ---- SGR mouse event fragments ----
	// SGR format: buttons;col;rowM (e.g. "65;25;31M" or "<65;25;31M").
	// When split at PTY buffer boundaries, fragments arrive in various forms:
	//   "65;25;31M" (full tail)        "5;42M" (one semicolon, partial)
	//   ";35;42M" (starts with ;)      ";35;42M35" (concatenated garbage)
	// Any sequence containing semicolon-delimited digits ending in M/m is
	// deterministically SGR residue — never legitimate keyboard input.
	if len(runes) >= 4 {
		s := string(runes)
		if looksLikeSGRFragment(s) {
			return true
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
//
// When terminal OSC color query responses (e.g. \x1b]11;rgb:fae0/fae0/fae0\x07)
// split across PTY buffer boundaries, partial "rgb:" prefix remnants like
// "b:fae0/fae0/fae0" or "gb:fae0/fae0/fae0" can leak through. We strip these
// known prefixes (and trailing ST terminator backslashes) before validating.
func isColorHexValue(s string) bool {
	// Strip trailing backslash from leaked ST terminator (ESC \)
	s = strings.TrimSuffix(s, "\\")

	// Strip known color prefixes (full and partial) that may survive PTY buffer splits.
	// "rgb:" is the standard prefix; "b:", "gb:", "g:", ":" are partial remnants
	// when the PTY splits at different points within "rgb:".
	for _, prefix := range []string{"rgba:", "rgb:", "gb:", "b:", "g:", ":"} {
		if strings.HasPrefix(s, prefix) {
			s = s[len(prefix):]
			break
		}
	}

	// Strip leading "/" from buffer splits at hex group boundaries.
	// e.g. the response "rgb:fae0/fae0/fae0" splitting as "rgb:fae0" + "/fae0/fae0"
	s = strings.TrimPrefix(s, "/")

	parts := strings.Split(s, "/")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	for _, p := range parts {
		if len(p) < 1 || len(p) > 4 {
			return false
		}
		for _, c := range p {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	// For 2-group fragments, require each group to be exactly 4 hex digits
	// to avoid false positives with short path-like input (e.g. "ab/cd").
	// Terminal color responses always use 4-digit hex (e.g. "fae0/fae0").
	if len(parts) == 2 {
		for _, p := range parts {
			if len(p) != 4 {
				return false
			}
		}
	}
	return true
}

// looksLikeSGRFragment checks if s looks like a fragment of an SGR mouse event
// that leaked past Bubble Tea's terminal layer. SGR mouse format is:
// buttons;col;rowM  (e.g. "65;25;31M") prefixed by ESC[<.
//
// We catch any split variant: full tails ("65;25;31M"), single-semicolon
// fragments ("5;42M"), semicolon-prefixed fragments (";35;42M"), and
// concatenated garbage (";35;42M35" from multiple events in one buffer read).
func looksLikeSGRFragment(s string) bool {
	nSemi := strings.Count(s, ";")
	if nSemi == 0 || nSemi > 4 {
		return false
	}
	last := s[len(s)-1]
	if last != 'M' && last != 'm' {
		return false
	}
	// The last segment before M/m must be digits only
	parts := strings.Split(s, ";")
	lastPart := parts[len(parts)-1]
	lastPart = lastPart[:len(lastPart)-1] // strip trailing M/m
	if !isAllDigits(lastPart) {
		return false
	}
	// All other segments must be digits (or start with < which is the SGR prefix)
	for i := 0; i < len(parts)-1; i++ {
		seg := parts[i]
		if len(seg) > 0 && seg[0] == '<' {
			seg = seg[1:]
		}
		if len(seg) > 0 && !isAllDigits(seg) {
			return false
		}
	}
	return true
}

// isOSCContinuation checks if runes look like the body of an OSC sequence
// that follows ESC ]. After Bubble Tea consumes ESC, ']' arrives as a lone
// KeyRunes event, followed by the OSC number (digits) and parameters. If the
// batch starts with a digit, it's almost certainly an OSC continuation and
// should be discarded along with the held ']'.
func isOSCContinuation(runes []rune) bool {
	return len(runes) > 0 && runes[0] >= '0' && runes[0] <= '9'
}

// isAllBackslash returns true if all runes in the slice are backslash (\) characters.
// Used to detect leaked escape sequence terminators (ST = ESC \) from DCS/OSC
// sequences that split across PTY buffer boundaries.
func isAllBackslash(runes []rune) bool {
	for _, r := range runes {
		if r != '\\' {
			return false
		}
	}
	return len(runes) > 0
}

// isAllDigits returns true if s is non-empty and contains only ASCII digits 0-9.
func isAllDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isSGRContinuation returns true if s consists only of digits and semicolons —
// the exact characters found in SGR mouse sequence fragments that leak past
// Bubble Tea's '[' tracker on Windows ConPTY. The SGR format is:
//
//	ESC [ < buttons ; col ; row M
//
// After ESC is consumed and the initial '[' + '<' are caught, the remaining
// fragments ("65", ";25", ";31M", ";") arrive as separate KeyRunes batches.
// This function catches the numeric-only fragments that don't end in M/m
// (those are handled by looksLikeSGRFragment).
//
// Callers MUST only invoke this when afterResidue is true, ensuring the
// window of false-positive risk is bounded to 1-3 batches after confirmed
// escape residue.
func isSGRContinuation(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != ';' {
			return false
		}
	}
	return true
}

// isOSCColorContinuation returns true if s consists only of characters found
// in OSC color response bodies: hex digits (0-9, a-f, A-F), '/', ':', '\',
// ';', and the literal letters 'r', 'g', 'b' (case-insensitive) from the
// "rgb:" or "rgba:" color format prefix.
//
// This is used when afterResidue is true to catch fragments of split OSC color
// responses (e.g. "/fae0/fae0\", "0/fae0/fae0", ":fae0/fae0/fae0\",
// ";rgb:fae0/fae0/fae0") that don't match isSGRContinuation (which only
// allows digits and semicolons).
//
// On macOS, terminal color query responses like \x1b]11;rgb:fae0/fae0/fae0\x1b\
// can split across PTY buffer boundaries into many small KeyRunes batches.
// After the initial OSC prefix is caught (] + digits), these continuation
// fragments must also be discarded. The "rgb:" prefix contains 'r' and 'g'
// which are OUTSIDE the hex range (a-f), so they must be explicitly allowed.
//
// Callers MUST only invoke this when afterResidue is true, ensuring the
// window of false-positive risk is bounded to batches immediately following
// confirmed escape residue.
func isOSCColorContinuation(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		case c == '/' || c == ':' || c == '\\' || c == ';':
		case c == 'r' || c == 'g' || c == 'R' || c == 'G':
		default:
			return false
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
)

// InputBuffer manages the text input state: text buffer, cursor, and selection.
type InputBuffer struct {
	text   []rune
	cursor int

	// Pasting is set true while bracketed paste is active so that
	// pasted newlines insert \n rather than triggering submit.
	Pasting bool
}

func NewInputBuffer() *InputBuffer {
	return &InputBuffer{}
}

func (ib *InputBuffer) Value() string {
	return string(ib.text)
}

func (ib *InputBuffer) SetValue(s string) {
	ib.text = []rune(s)
	ib.cursor = len(ib.text)
}

func (ib *InputBuffer) Cursor() int {
	return ib.cursor
}

func (ib *InputBuffer) Len() int {
	return len(ib.text)
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
		if ib.Pasting {
			// During bracketed paste, newlines are literal content
			ib.insertAtCursor('\n')
			return ActionNewline
		}
		// Plain Enter -> submit
		return ActionSubmit

	case msg.Type == tea.KeyEnter && msg.Alt:
		// Option/Alt+Enter -> newline (Mac Option key = Alt in terminals)
		ib.insertAtCursor('\n')
		return ActionNewline

	case msg.Type == tea.KeyBackspace:
		if ib.cursor > 0 {
			ib.text = append(ib.text[:ib.cursor-1], ib.text[ib.cursor:]...)
			ib.cursor--
		}
		return ActionBackspace

	case msg.Type == tea.KeyDelete:
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
		// Always preserve \n in KeyRunes — it never comes from keyboard input,
		// only from bracketed paste (WithBracketedPaste). Likewise convert \r to \n.
		filtered := make([]rune, 0, len(runes))
		for _, r := range runes {
			if r == '\n' || r == '\r' {
				filtered = append(filtered, '\n')
			} else if !isLikelyControlOrphan(r) {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) > 0 {
			ib.insertRunes(filtered)
			return ActionRuneInserted
		}
		return ActionNone

	case msg.Type == tea.KeySpace:
		ib.insertAtCursor(' ')
		return ActionRuneInserted
	}

	return ActionNone
}

