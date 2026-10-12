package ui

import (
	"fmt"
	"runtime"
	"strings"
	"time"

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

// pasteThreshold is the minimum newline count or total rune count to trigger
// paste-shorthand mode (show line count instead of the full pasted content
// in the input box).
const pasteThresholdNewlines = 3 // ≥3 newlines → paste
const pasteThresholdRunes = 200  // ≥200 runes → paste

// pasteGap is the maximum interval between two input events for them to be
// considered part of the same input run. Human typing gaps are typically
// 100ms+; terminal paste events arrive essentially back-to-back (sub-millisecond
// on Unix, a few ms apart on Windows ConPTY). 100ms safely separates the two.
//
// On Windows, Bubble Tea's coninput reader bypasses the ANSI parser, so
// bracketed-paste markers (\x1b[200~...\x1b[201~) never arrive as a single
// KeyRunes event with msg.Paste set — each pasted character (including each
// newline) arrives as a separate KeyEventRecord. We MUST use this time window
// to detect pastes on Windows, otherwise the first pasted newline would
// immediately trigger ActionSubmit.
const pasteGap = 100 * time.Millisecond

// pasteIdleGap is how long an input run that has ALREADY been classified as a
// paste may go quiet — an event arriving late because the console, the
// scheduler or a render stall delayed it — before the run counts as over.
// Ending the run at pasteGap instead would chop one paste into several
// "[Pasted +N lines]" indicators and spill pasted content into the input as raw
// text.
//
// The grace is spent on the first quiet event: absorbing one clears the run's
// paste counters, so a later quiet event only counts again once the run looks
// like a paste anew. The cost is bounded to that single event: if the paste had
// already ended, one character of the user's own typing is folded into the
// indicator, and everything typed after it is shown as text again. Without that
// bound, a user typing at human speed after a paste would stay inside the
// grace forever and never see their own text.
//
// Enter deliberately keeps the tighter pasteGap rule: a pasted newline delayed
// by a hiccup may still submit, but the far more common "paste, then press
// Enter to send" keeps working on the first press.
const pasteIdleGap = 500 * time.Millisecond

// inputSegment is one piece of PasteMode input. Typed text segments render
// as-is; paste segments render as a "[Pasted +N lines]" indicator. The raw
// text of every segment is concatenated on submit.
type inputSegment struct {
	text    string // segment content (typed text or pasted content)
	isPaste bool   // true renders as a paste indicator
}

// InputBuffer manages the text input state: text buffer, cursor, and selection.
type InputBuffer struct {
	text   []rune
	cursor int

	// Paste shorthand mode: when a large paste is detected, store the full
	// content here and show a "[Pasted +N lines]" indicator in the input.
	PasteContent string // full content to submit (prefix + pasted + suffix)
	PasteMode    bool   // when true, show paste indicator in input
	pastePrefix  string // text typed before the paste, shown before indicator
	// suffixSegments holds, in order, the input after pastePrefix: typed text
	// (rendered as-is) and paste chunks (rendered as "[Pasted +N lines]"
	// indicators). Each bracketed paste starts a NEW paste segment so every
	// paste keeps its own indicator.
	suffixSegments []inputSegment

	// burst is the current uninterrupted run of input events — the unit the
	// paste heuristic judges (see inputBurst).
	burst inputBurst

	// pasteStreamActive is true while a paste is still arriving event by event
	// (Windows ConPTY without bracketed paste). A live event in PasteMode then
	// continues the current paste segment. After a bracketed paste (msg.Paste)
	// the full content arrived in one event, so pasteStreamActive is false and
	// later events are the user typing.
	pasteStreamActive bool
}

// inputBurst is one uninterrupted run of input events: the terminal delivering
// a paste character by character (Windows ConPTY, where msg.Paste is never set)
// or the user typing. It is the unit the paste heuristic judges — never text
// that predates it — so lines typed or pasted earlier can no longer make the
// current run look like a paste.
type inputBurst struct {
	active   bool      // a run is in progress (lastAt is meaningful)
	lastAt   time.Time // timestamp of the run's most recent event
	startLen int       // len(text) when the run began: text[:startLen] is the
	// user's pre-run input, text[startLen:] was inserted by this run
	runes     int  // runes inserted by this run
	newlines  int  // newlines inserted by this run
	confirmed bool // the run already looks like a paste rather than typing
}

// beginBurst starts a run whose content begins at the current end of the
// buffer: everything already in the buffer is the user's pre-run input (shown
// as the prefix before a paste indicator).
func (ib *InputBuffer) beginBurst() {
	ib.burst = inputBurst{active: true, startLen: len(ib.text)}
}

// endBurst closes the current run. The next event opens a fresh one, so neither
// its timing nor its counters can continue the closed run.
func (ib *InputBuffer) endBurst() {
	ib.burst = inputBurst{}
}

// noteEvent records one input event in the run timeline. An event arriving
// within pasteGap of the previous one continues the run; anything slower opens
// a new run. Returns whether the existing run was continued.
func (ib *InputBuffer) noteEvent(hot bool, now time.Time) bool {
	if !hot {
		ib.beginBurst()
	}
	ib.burst.lastAt = now
	return hot
}

// growBurst accounts the runes inserted by the current run and confirms the run
// as a paste once it crosses a threshold. Both thresholds are evaluated on the
// run itself, never on the whole buffer.
func (ib *InputBuffer) growBurst(runes, newlines int) {
	ib.burst.runes += runes
	ib.burst.newlines += newlines
	if ib.burst.newlines >= pasteThresholdNewlines || ib.burst.runes >= pasteThresholdRunes {
		ib.burst.confirmed = true
	}
}

// absorbHiccup keeps a confirmed, still-streaming paste in one piece when an
// event arrives after more than pasteGap (a delivery or scheduling hiccup).
// The run stays open but its paste counters are cleared, which spends the
// grace: a later slow event is treated as the user again unless the run
// rebuilds enough paste-shaped content to be confirmed once more.
func (ib *InputBuffer) absorbHiccup(now time.Time) {
	ib.burst.runes = 0
	ib.burst.newlines = 0
	ib.burst.confirmed = false
	ib.burst.lastAt = now
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
	ib.PasteMode = false
	ib.PasteContent = ""
	ib.pastePrefix = ""
	ib.suffixSegments = nil
	ib.endBurst()
}

// SubmitContent returns the content to submit. In paste shorthand mode it
// returns the full pasted content; otherwise returns the input buffer text.
// After calling, paste mode is cleared regardless.
func (ib *InputBuffer) SubmitContent() string {
	if ib.PasteMode {
		content := ib.PasteContent
		ib.PasteMode = false
		ib.PasteContent = ""
		ib.pastePrefix = ""
		ib.suffixSegments = nil
		ib.text = nil
		ib.cursor = 0
		ib.endBurst()
		return content
	}
	return ib.Value()
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

// enterPasteMode splits the current text at the run boundary: the prefix
// (input that predates the run) is preserved and shown before the indicator;
// the rest (the run's own content) becomes the first paste segment.
func (ib *InputBuffer) enterPasteMode(fullText string, streamActive bool) {
	prefix := ""
	pasted := fullText
	textRunes := []rune(fullText)
	if ib.burst.startLen > 0 && ib.burst.startLen <= len(textRunes) {
		prefix = string(textRunes[:ib.burst.startLen])
		pasted = string(textRunes[ib.burst.startLen:])
	}
	ib.pastePrefix = prefix
	ib.suffixSegments = []inputSegment{{text: pasted, isPaste: true}}
	ib.pasteStreamActive = streamActive
	ib.PasteMode = true
	ib.rebuild()
}

// rebuild regenerates the visible text (pastePrefix + rendered segments) and
// the full PasteContent (pastePrefix + every segment's raw text). Cursor is
// placed at the end so new input appends after the last segment.
//
// The paste indicator is embedded as "dimOpen + plain marker + reopen" rather
// than PasteIndicatorStyle.Render(...) directly. Render() emits a trailing
// \x1b[0m which, when the visible text is nested inside InputBlockStyle.Render
// in renderInputLine, resets the input block's background mid-line — the rest
// of the line (and the wrapped lines after it) would lose their background.
func (ib *InputBuffer) rebuild() {
	// ANSI open sequences for embedding a styled paste marker inline without
	// a bare reset: dimOpen starts the dim foreground, reopen restores the
	// input block's foreground+background right after the marker.
	dimOpen := strings.TrimSuffix(PasteIndicatorStyle.Render(""), "\x1b[0m")
	reopen := strings.TrimSuffix(InputBlockStyle.Render(""), "\x1b[0m")

	var vis, content strings.Builder
	vis.WriteString(ib.pastePrefix)
	content.WriteString(ib.pastePrefix)
	for _, seg := range ib.suffixSegments {
		content.WriteString(seg.text)
		if seg.isPaste {
			lines := strings.Count(seg.text, "\n") + 1
			vis.WriteString(dimOpen)
			vis.WriteString(fmt.Sprintf("[Pasted +%d lines]", lines))
			vis.WriteString(reopen)
		} else {
			vis.WriteString(seg.text)
		}
	}
	ib.text = []rune(vis.String())
	ib.cursor = len(ib.text)
	ib.PasteContent = content.String()
}

// appendTextSegment appends typed text, merging into the trailing text segment
// if one exists, so successive typing stays in one visible chunk.
func (ib *InputBuffer) appendTextSegment(s string) {
	if n := len(ib.suffixSegments); n > 0 {
		last := &ib.suffixSegments[n-1]
		if !last.isPaste {
			last.text += s
			return
		}
	}
	ib.suffixSegments = append(ib.suffixSegments, inputSegment{text: s})
}

// appendPasteSegment appends pasted content. A bracketed paste always starts a
// NEW paste segment (new indicator); live events from a paste that is still
// arriving (Windows ConPTY without bracketed paste) continue the current one.
func (ib *InputBuffer) appendPasteSegment(s string, forceNew bool) {
	if !forceNew && len(ib.suffixSegments) > 0 {
		last := &ib.suffixSegments[len(ib.suffixSegments)-1]
		if last.isPaste {
			last.text += s
			return
		}
	}
	ib.suffixSegments = append(ib.suffixSegments, inputSegment{text: s, isPaste: true})
}

// deleteFromLastTextSegment removes the last rune of the trailing text segment.
// Returns false when there is nothing to delete (the input ends with a paste
// indicator or is empty), in which case the caller cancels the whole paste.
func (ib *InputBuffer) deleteFromLastTextSegment() bool {
	n := len(ib.suffixSegments)
	if n == 0 {
		return false
	}
	last := &ib.suffixSegments[n-1]
	if last.isPaste || len(last.text) == 0 {
		return false
	}
	r := []rune(last.text)
	r = r[:len(r)-1]
	last.text = string(r)
	if len(last.text) == 0 {
		ib.suffixSegments = ib.suffixSegments[:n-1]
	}
	return true
}

// deleteTrailingPasteSegment removes the trailing paste segment. Returns true
// when a paste segment was removed; false when the input does not end with a
// paste segment (or is empty). When the removed segment was the last one and
// only the prefix remains, PasteMode ends and the prefix becomes plain input
// again.
func (ib *InputBuffer) deleteTrailingPasteSegment() bool {
	n := len(ib.suffixSegments)
	if n == 0 || !ib.suffixSegments[n-1].isPaste {
		return false
	}
	ib.suffixSegments = ib.suffixSegments[:n-1]
	if len(ib.suffixSegments) == 0 {
		// Nothing left but the prefix (or nothing at all) — leave PasteMode.
		prefix := ib.pastePrefix
		ib.PasteMode = false
		ib.pastePrefix = ""
		ib.PasteContent = ""
		ib.endBurst()
		ib.pasteStreamActive = false
		ib.text = []rune(prefix)
		ib.cursor = len(ib.text)
		return true
	}
	ib.rebuild()
	return true
}

// cancelPaste clears all paste state, returning the input to empty.
func (ib *InputBuffer) cancelPaste() {
	ib.PasteMode = false
	ib.PasteContent = ""
	ib.pastePrefix = ""
	ib.suffixSegments = nil
	ib.text = nil
	ib.cursor = 0
	ib.endBurst()
}

// HandleKey processes a keyboard event and returns the action to take.
// The buffer is mutated inline for insert/delete/cursor operations.
//
// Paste detection strategy:
//   - Bracketed paste (Unix/macOS): the whole paste arrives as a single
//     KeyRunes event with msg.Paste == true. We check the batch size.
//   - Time-window run (Windows ConPTY, or terminals without bracketed
//     paste): pastes arrive as a rapid series of KeyRunes/KeyEnter events,
//     each within pasteGap of the previous. That run is tracked in
//     InputBuffer.burst; once it accumulates ≥3 newlines or ≥200 runes the
//     input switches to PasteMode. Only the run's own content is counted, so
//     lines typed or pasted earlier can never fold the current run.
//   - A deliberate newline (Alt/Shift/Ctrl+Enter) closes the run, so the
//     keystrokes after it are never mistaken for pasted content.
//
// IME safety: runes are ALWAYS inserted into ib.text immediately. The run
// detector only decides whether an Enter inserts \n (paste) or submits — it
// never defers rune insertion. IME composition runs (which contain no
// Enter keys) are therefore never misclassified as paste.
//
// PasteMode display: once entered, the visible input is a series of segments:
// paste chunks render as "[Pasted +N lines]" indicators and user-typed text
// renders as-is (e.g. "123 [Pasted +4 lines] 456 [Pasted +3 lines] 789").
// Each bracketed paste starts a NEW indicator; quiet events (user typing after
// the run) become text segments; live events (still inside the paste stream)
// continue the current paste segment. Only Enter submits, Backspace cancels.
func (ib *InputBuffer) HandleKey(msg tea.KeyMsg) InputAction {
	now := time.Now()
	// hot: this event continues the run already in progress.
	hot := ib.burst.active && now.Sub(ib.burst.lastAt) < pasteGap
	// hiccup: a confirmed paste that is still streaming may go quiet for longer
	// than pasteGap without being over (see pasteIdleGap). Only content travels
	// through this window — Enter keeps the tighter hot rule.
	hiccup := !hot && ib.burst.active && ib.burst.confirmed && ib.pasteStreamActive &&
		now.Sub(ib.burst.lastAt) < pasteIdleGap

	// ---- PasteMode: indicator stays, new input appends as segments ----
	if ib.PasteMode {
		switch msg.Type {
		case tea.KeyEnter:
			if msg.Alt || (!hot && runtime.GOOS == "windows" && ctrlKeyPressed()) {
				// Deliberate manual newline — append visibly as a text segment and
				// close the run, so the Enter after it submits.
				ib.endBurst()
				ib.appendTextSegment("\n")
				ib.rebuild()
				return ActionNewline
			}
			if hot {
				// More paste arriving — absorb newline into the current paste segment.
				ib.appendPasteSegment("\n", false)
				ib.growBurst(1, 1)
				ib.burst.lastAt = now
				ib.rebuild()
				return ActionNewline
			}
			// Slow Enter — submit the accumulated content.
			return ActionSubmit

		case tea.KeyBackspace:
			if ib.deleteFromLastTextSegment() {
				ib.rebuild()
				ib.burst.lastAt = now
				return ActionBackspace
			}
			// Trailing segment is a paste indicator (or nothing) — remove the
			// paste segment itself, keeping the prefix and earlier segments.
			if ib.deleteTrailingPasteSegment() {
				ib.endBurst()
				return ActionBackspace
			}
			// Nothing to delete at all — cancel the paste entirely (clear input).
			ib.cancelPaste()
			ib.endBurst()
			return ActionBackspace

		case tea.KeyRunes:
			runes := msg.Runes
			if isTerminalEscapeResidue(runes) {
				return ActionNone
			}
			filtered := make([]rune, 0, len(runes))
			for _, r := range runes {
				if r == '\n' || r == '\r' {
					filtered = append(filtered, '\n')
				} else if !isLikelyControlOrphan(r) {
					filtered = append(filtered, r)
				}
			}
			if len(filtered) == 0 {
				return ActionNone
			}
			nNewlines := strings.Count(string(filtered), "\n")
			switch {
			case msg.Paste:
				// A bracketed paste arrives complete in one event: it opens a NEW
				// run and a NEW paste segment so it gets its own indicator (Bug:
				// multiple pastes folded into one).
				ib.beginBurst()
				ib.appendPasteSegment(string(filtered), true)
				ib.growBurst(len(filtered), nNewlines)
				ib.burst.lastAt = now
			case (hot || hiccup) && ib.pasteStreamActive:
				// A live event while a paste is still arriving event by event
				// (Windows ConPTY, no bracketed paste) — continue the current
				// paste segment (no new indicator). After a bracketed paste
				// (msg.Paste) the content arrived complete in one event, so
				// pasteStreamActive is false and this is the user typing: it
				// must go to a text segment, otherwise it creates a bogus
				// "[Pasted +1 lines]" indicator.
				if hiccup {
					ib.absorbHiccup(now)
				} else {
					ib.noteEvent(hot, now)
				}
				ib.appendPasteSegment(string(filtered), false)
				ib.growBurst(len(filtered), nNewlines)
			default:
				// User typing after the paste — show as a text segment. Typed
				// text never counts towards the paste thresholds.
				ib.noteEvent(hot, now)
				ib.appendTextSegment(string(filtered))
			}
			ib.rebuild()
			return ActionRuneInserted

		case tea.KeySpace:
			if (hot || hiccup) && ib.pasteStreamActive {
				if hiccup {
					ib.absorbHiccup(now)
				} else {
					ib.noteEvent(hot, now)
				}
				ib.appendPasteSegment(" ", false)
				ib.growBurst(1, 0)
			} else {
				ib.noteEvent(hot, now)
				ib.appendTextSegment(" ")
			}
			ib.rebuild()
			return ActionRuneInserted

		case tea.KeyDelete:
			// Bug #2: Delete in PasteMode deletes the last user-typed character
			// from the trailing text segment (same as Backspace). Since cursor is
			// always at end in PasteMode, Forward-Delete and Backspace are
			// semantically identical.
			if ib.deleteFromLastTextSegment() {
				ib.rebuild()
				ib.burst.lastAt = now
				return ActionDelete
			}
			// Trailing segment is a paste indicator (or nothing) — remove the
			// paste segment itself, keeping the prefix and earlier segments.
			if ib.deleteTrailingPasteSegment() {
				ib.endBurst()
				return ActionDelete
			}
			// Nothing to delete at all — cancel the paste entirely (clear input).
			ib.cancelPaste()
			ib.endBurst()
			return ActionDelete

		default:
			// Left/Right/Home/End are no-ops in PasteMode — the
			// indicator + segments display is append-only.
			return ActionNone
		}
	}

	// ---- Normal (non-paste) input handling ----
	switch {
	case msg.Type == tea.KeyEnter && !msg.Alt:
		// Windows: Ctrl+Enter = newline when NOT mid-run. Detected via physical
		// key state since bubbletea's KeyMsg lacks a Ctrl modifier field. Must
		// gate on !hot to avoid intercepting pasted newlines when the user
		// hasn't released Ctrl after Ctrl+V.
		if !hot && runtime.GOOS == "windows" && ctrlKeyPressed() {
			// Deliberate manual newline — a run boundary.
			ib.endBurst()
			ib.insertAtCursor('\n')
			return ActionNewline
		}
		if hot {
			// Pasted newline mid-run — insert as literal \n, don't submit.
			ib.insertAtCursor('\n')
			ib.growBurst(1, 1)
			ib.burst.lastAt = now
			if ib.burst.confirmed {
				ib.enterPasteMode(string(ib.text), true)
			}
			return ActionNewline
		}
		// Slow Enter — submit immediately.
		ib.endBurst()
		return ActionSubmit

	case msg.Type == tea.KeyEnter && msg.Alt:
		// Option/Alt/Shift+Enter -> newline (Mac Option key = Alt in
		// terminals). A deliberate manual newline closes the run: a quick
		// keystroke right after it must not look like pasted content, and the
		// run's newline count must not include it.
		ib.endBurst()
		ib.insertAtCursor('\n')
		return ActionNewline

	case msg.Type == tea.KeyBackspace:
		// (pendingSubmit was removed; normal backspace behavior applies.)
		if ib.cursor > 0 {
			ib.text = append(ib.text[:ib.cursor-1], ib.text[ib.cursor:]...)
			ib.cursor--
		}
		ib.endBurst()
		return ActionBackspace

	case msg.Type == tea.KeyDelete:
		ib.endBurst()
		if ib.cursor < len(ib.text) {
			ib.text = append(ib.text[:ib.cursor], ib.text[ib.cursor+1:]...)
		}
		return ActionDelete

	case msg.Type == tea.KeyLeft:
		ib.endBurst()
		if ib.cursor > 0 {
			ib.cursor--
		}
		return ActionCursorLeft

	case msg.Type == tea.KeyRight:
		ib.endBurst()
		if ib.cursor < len(ib.text) {
			ib.cursor++
		}
		return ActionCursorRight

	case msg.Type == tea.KeyHome:
		ib.endBurst()
		ib.cursor = 0
		return ActionCursorHome

	case msg.Type == tea.KeyEnd:
		ib.endBurst()
		ib.cursor = len(ib.text)
		return ActionCursorEnd

	case msg.Type == tea.KeyRunes:
		// Filter out control characters and escape sequences that leak from
		// terminal responses (e.g., OSC 11 color query, SGR mouse events).
		runes := msg.Runes
		if isTerminalEscapeResidue(runes) {
			return ActionNone
		}
		filtered := make([]rune, 0, len(runes))
		for _, r := range runes {
			if r == '\n' || r == '\r' {
				filtered = append(filtered, '\n')
			} else if !isLikelyControlOrphan(r) {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) == 0 {
			return ActionNone
		}

		// A bracketed paste arrives complete in a single event: it opens its own
		// run, so its content can never be attributed to earlier typing. Every
		// other event continues the current run or opens a new one.
		if msg.Paste {
			ib.beginBurst()
		} else {
			ib.noteEvent(hot, now)
		}

		// Insert runes into text immediately (IME-safe: no deferred buffer).
		ib.insertRunes(filtered)
		ib.growBurst(len(filtered), strings.Count(string(filtered), "\n"))
		ib.burst.lastAt = now

		// A run that accumulated paste-shaped content switches to the paste
		// shorthand: either a single large bracketed-paste batch (msg.Paste,
		// Unix/macOS) or a run whose own newline/rune count crossed a threshold.
		if ib.burst.confirmed {
			ib.enterPasteMode(string(ib.text), !msg.Paste)
		}
		return ActionRuneInserted

	case msg.Type == tea.KeySpace:
		// A space is ordinary input: like a rune, it continues or opens a run,
		// so pasted text containing spaces stays attributed to the run.
		ib.noteEvent(hot, now)
		ib.insertAtCursor(' ')
		ib.growBurst(1, 0)
		return ActionRuneInserted
	}

	return ActionNone
}
