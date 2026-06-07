package ui

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/deepact/deepact/engine"
)

type AppState int

const (
	stateInit AppState = iota
	stateApiKeyPrompt
	stateReady
	stateRunning
)

type DisplayMessage struct {
	Role    string
	Content string
}

type AgentSpinner struct {
	Role     string
	Goal     string
	Summary  string
	Active   bool
	FrameIdx int
}

type ToolNode struct {
	Name       string
	Detail     string // bash command / file path / pattern
	DetailFull string // full diff / output content
	Done       bool
	Icon       string     // 📝 💻 🔍 📖
	Children   []ToolNode // diff hunks for edit/write, output for bash
}

type StatusInfo struct {
	Model        string
	TokensIn     int
	TokensOut    int
	Cost         float64
	SessionCost  float64
	AgentStatus  string
	ExtraMessage string
}

type Suggestion struct {
	Command     string // e.g. "/规划"
	Args        string // e.g. "<需求描述>"
	Description string // e.g. "分析需求，探索代码并制定方案"
}

var slashCommands = []Suggestion{
	{Command: "/help", Args: "", Description: "Show this help screen"},
}

type Model struct {
	state        AppState
	messages     []DisplayMessage
	inputBuf     *InputBuffer
	status       StatusInfo
	spinners     []AgentSpinner
	toolTree     []ToolNode
	width        int
	height       int
	engine       EngineRunner
	streaming    string
	apiKeyInput           string
	pendingOpenBracket    bool   // Windows: lone '[' held to check if it's escape split
	pendingCloseBracket   bool   // lone ']' held to check if it's OSC escape (ESC ] Ps ; Pt ST)
	afterResidue          bool   // Mac: tracks if prev batch was escape residue (for ST terminator \ filtering)
	ready                 bool
	progressChan chan ProgressMsg
	scrollOffset int
	cancelled    bool
	pendingEsc   bool // tracks ESC prefix for Alt+Enter sequence detection
	pricing      engine.PricingConfig

	// Slash command suggestions
	showSuggestions    bool
	suggestions        []Suggestion
	selectedSuggestion int

	// External skill names (loaded from .deepact/skills/) for / suggestions
	skillSuggestions []Suggestion

	// Active options (plan selection / review actions)
	activeOptions  []string
	selectedOption int
}

type ProgressMsg struct {
	Type       string
	Name       string
	Detail     string
	FullDetail string
	TokensIn   int
	TokensOut  int
	CacheHit   int
	ModelName  string
}

const (
	logoDelay   = 500 * time.Millisecond
	spinnerRate = 100 * time.Millisecond
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func NewModel(runner EngineRunner, pricing engine.PricingConfig) Model {
	progressChan := make(chan ProgressMsg, 32)
	if runner != nil {
		runner.SetProgressChan(progressChan)
	}
	return Model{
		state:    stateInit,
		messages: []DisplayMessage{},
		inputBuf: NewInputBuffer(),
		status: StatusInfo{
			Model:       "pro",
			TokensIn:    0,
			TokensOut:   0,
			Cost:        0,
			SessionCost: 0,
		},
		engine:       runner,
		progressChan: progressChan,
		pricing:      pricing,
	}
}

func (m Model) ProgressChan() chan ProgressMsg {
	return m.progressChan
}

func (m Model) Init() tea.Cmd {
	return tea.Tick(logoDelay, func(time.Time) tea.Msg {
		return TickMsg{}
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Clear transient input flags on non-key events. This prevents stale
	// pendingEsc from persisting across TickMsg or WindowSizeMsg events,
	// which would cause the next Enter to insert a newline instead of submit.
	//
	// afterResidue is NOT cleared here — it must survive non-KeyMsg events
	// (ProgressMsg, TickMsg) that frequently arrive between escape residue
	// and its trailing ST backslash (ESC \), so the \ can still be caught.
	switch msg.(type) {
	case tea.KeyMsg:
		// flags are managed in handleKey
	default:
		m.pendingEsc = false
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if m.state == stateReady || m.state == stateRunning {
				oldOff := m.scrollOffset
				m.scrollOffset += m.height / 3
				if m.scrollOffset < oldOff {
					m.scrollOffset = oldOff // overflow guard
				}
			}
			return m, nil
		case tea.MouseButtonWheelDown:
			if m.state == stateReady || m.state == stateRunning {
				m.scrollOffset -= m.height / 3
				if m.scrollOffset < 0 {
					m.scrollOffset = 0
				}
			}
			return m, nil
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		return m, nil
	case TickMsg:
		return m.handleTick()
	case StreamDeltaMsg:
		m.streaming += msg.Content
		return m, nil
	case ToolStartMsg:
		m.toolTree = append(m.toolTree, ToolNode{Name: msg.Name, Detail: msg.Args, Icon: toolIcon(msg.Name)})
		return m, nil
	case ToolDoneMsg:
		for i := range m.toolTree {
			if m.toolTree[i].Name == msg.Name && !m.toolTree[i].Done {
				m.toolTree[i].Done = true
				m.toolTree[i].DetailFull = msg.Digest
				// For edit/write, try parsing diff content
				switch m.toolTree[i].Name {
				case "edit", "write":
					m.toolTree[i].Children = parseDiffHunks(msg.Digest)
				case "bash":
					m.toolTree[i].Children = parseOutputLines(msg.Digest, 10)
				}
				break
			}
		}
		return m, nil
	case AgentStartMsg:
		m.spinners = append(m.spinners, AgentSpinner{Role: msg.Role, Goal: msg.Goal, Active: true})
		return m, nil
	case AgentDoneMsg:
		for i := range m.spinners {
			if m.spinners[i].Role == msg.Role && m.spinners[i].Active {
				m.spinners[i].Active = false
				m.spinners[i].Summary = msg.Summary
				break
			}
		}
		return m, nil
	case EngineResponseMsg:
		if m.cancelled {
			m.cancelled = false
			return m, nil
		}
		m.state = stateReady
		// Only reset scroll if user wasn't reading history
		if m.scrollOffset <= 0 {
			m.scrollOffset = 0
		}
		m.finishStreaming(msg)
		return m, nil
	case ProgressMsg:
		// Auto-scroll to bottom while running so new tool/spinner content stays visible.
		// But only if user hasn't manually scrolled up to read history.
		if m.state == stateRunning && m.scrollOffset <= 0 {
			m.scrollOffset = 0
		}
		switch msg.Type {
		case "thinking":
			if len(m.spinners) > 0 {
				m.spinners[0].Goal = msg.Detail
			}
		case "conference_enter":
			// Show a prominent header when entering multi-agent conference mode
			// msg.Detail contains localized text like "进入多智能体会议模式"
			detail := msg.Detail
			if detail == "" {
				detail = fmt.Sprintf("🧠 Multi-Agent Conference — %s Phase", msg.Name)
			}
			m.messages = append(m.messages, DisplayMessage{Role: "system", Content: detail})
			if len(m.spinners) > 0 {
				m.spinners[0].Goal = "🧠 " + msg.Name
			}
		case "conference_phase":
			// Update phase label during conference execution
			phaseLabel := msg.Name
			detail := msg.Detail
			if detail == "" {
				detail = phaseLabel
			}
			if len(m.spinners) > 0 {
				m.spinners[0].Goal = "🧠 " + detail
			}
		case "agent_start":
			displayName := "🧠 " + msg.Name
			goal := msg.Detail
			if len(goal) > 60 {
				goal = goal[:60] + "..."
			}
			m.toolTree = append(m.toolTree, ToolNode{Name: displayName, Detail: goal})
			if len(m.spinners) > 0 {
				m.spinners[0].Goal = displayName + " running..."
			}
		case "agent_done":
			displayName := "🧠 " + msg.Name
			for i := range m.toolTree {
				if m.toolTree[i].Name == displayName && !m.toolTree[i].Done {
					m.toolTree[i].Done = true
					summary := msg.Detail
					if len(summary) > 80 {
						summary = summary[:80] + "..."
					}
					m.toolTree[i].Detail = "✓ done — " + summary
					break
				}
			}
			if len(m.spinners) > 0 {
				m.spinners[0].Goal = displayName + " ✓"
			}
		case "tool_start":
			m.toolTree = append(m.toolTree, ToolNode{Name: msg.Name, Detail: msg.Detail, Icon: toolIcon(msg.Name)})
			if len(m.spinners) > 0 {
				m.spinners[0].Goal = msg.Name + ": " + msg.Detail
			}
		case "tool_done":
			for i := range m.toolTree {
				if m.toolTree[i].Name == msg.Name && !m.toolTree[i].Done {
					m.toolTree[i].Done = true
					m.toolTree[i].DetailFull = msg.FullDetail
					// Parse children based on tool type
					switch m.toolTree[i].Name {
					case "edit", "write":
						m.toolTree[i].Children = parseDiffHunks(msg.FullDetail)
					case "bash":
						m.toolTree[i].Children = parseOutputLines(msg.FullDetail, 10)
					}
					break
				}
			}
			if len(m.spinners) > 0 {
				m.spinners[0].Goal = msg.Name + " ✓"
			}
		case "stream_delta":
			// Progressive text output from sub-agents (searcher, brainstorm, etc.)
			m.streaming += msg.Detail
		case "usage":
			m.status.TokensIn += msg.TokensIn
			m.status.TokensOut += msg.TokensOut
			cost := estimateCost(msg.TokensIn, msg.TokensOut, msg.CacheHit, msg.ModelName, &m.pricing)
			m.status.Cost = cost
			m.status.SessionCost += cost
		case "skill_activated":
			m.messages = append(m.messages, DisplayMessage{
				Role:    "system",
				Content: fmt.Sprintf("🧠 Skill activated: **%s** — %s", msg.Name, msg.Detail),
			})
		}
		return m, waitForProgress(m.progressChan)
	case StatusUpdateMsg:
		m.status = msg.Info
		return m, nil
	case ApiKeySetMsg:
		if engineFactory == nil {
			m.messages = append(m.messages, DisplayMessage{Role: "system", Content: "engine factory not configured"})
			return m, nil
		}
		runner, err := engineFactory(msg.Key)
		if err != nil {
			m.messages = append(m.messages, DisplayMessage{Role: "system", Content: fmt.Sprintf("engine init failed: %v", err)})
			return m, nil
		}
		runner.SetProgressChan(m.progressChan)
		m.engine = runner
		m.state = stateReady
		m.apiKeyInput = ""
		return m, nil
	}
	return m, nil
}

// scrollUp increases scroll offset by half the terminal height.
func (m Model) scrollUp() Model {
	m.scrollOffset += m.height / 2
	return m
}

// scrollDown decreases scroll offset by half the terminal height, clamped at 0.
func (m Model) scrollDown() Model {
	m.scrollOffset -= m.height / 2
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
	return m
}

func (m Model) View() string {
	if !m.ready {
		return "loading..."
	}

	contentWidth := m.width
	if contentWidth <= 0 {
		contentWidth = 80
	}

	// API key prompt: full-screen centered, no bottom bars, no cursor blink
	if m.state == stateApiKeyPrompt {
		return strings.Join(m.renderBody(contentWidth), "\n")
	}

	suggestionPopup := renderSuggestions(m, contentWidth)
	optionsPopup := renderOptionsPopup(m, contentWidth)

	// Render footer first to measure actual heights.
	// First pass: compute status bar height (no scroll info yet).
	statusLine := renderStatusBar(m.status, m.scrollOffset, 0, contentWidth)
	inputLine := renderInputLine(m)
	actualStatusHeight := renderedHeight(statusLine)
	actualInputHeight := renderedHeight(inputLine)

	// Only count popup lines for popups that are actually shown
	bodyHeight := m.height - actualStatusHeight - actualInputHeight
	if suggestionPopup != "" {
		suggestionHeight := renderedHeight(suggestionPopup)
		bodyHeight -= suggestionHeight
	}
	if optionsPopup != "" {
		optionsHeight := renderedHeight(optionsPopup)
		bodyHeight -= optionsHeight
	}
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	// First pass: render at full width to check overflow
	needScrollbar := false
	scrollContentWidth := contentWidth

	lines := m.renderBody(contentWidth)
	total := len(lines)
	maxScroll := 0
	scrollOff := m.scrollOffset
	if total > bodyHeight {
		needScrollbar = true
		scrollContentWidth = contentWidth - 1
		if scrollContentWidth < 20 {
			scrollContentWidth = 20
		}
		// Re-render at 1-char-less width so the scrollbar fits without overflow
		lines = m.renderBody(scrollContentWidth)
		total = len(lines)
		maxScroll = total - bodyHeight
		if maxScroll < 0 {
			maxScroll = 0
		}
		if scrollOff > maxScroll {
			scrollOff = maxScroll
		}
		if scrollOff < 0 {
			scrollOff = 0
		}
		end := total - scrollOff
		start := end - bodyHeight
		if start < 0 {
			start = 0
		}
		lines = lines[start:end]
	}
	// Second pass: re-render status bar with actual scroll info
	statusLine = renderStatusBar(m.status, scrollOff, maxScroll, contentWidth)
	// Re-check: if scroll info changed the status bar height (e.g. scroll hint
	// wrapping on narrow terminals), re-clip body to keep total = m.height.
	if newStatusH := renderedHeight(statusLine); newStatusH != actualStatusHeight {
		bodyHeight = m.height - newStatusH - actualInputHeight
		if suggestionPopup != "" {
			bodyHeight -= renderedHeight(suggestionPopup)
		}
		if optionsPopup != "" {
			bodyHeight -= renderedHeight(optionsPopup)
		}
		if bodyHeight < 1 {
			bodyHeight = 1
		}
		// Re-clip lines to new bodyHeight
		if len(lines) > bodyHeight {
			lines = lines[:bodyHeight]
		} else {
			for len(lines) < bodyHeight {
				lines = append(lines, "")
			}
		}
		// Recalculate maxScroll from original total (not clipped lines)
		if total > 0 {
			maxScroll = total - bodyHeight
			if maxScroll < 0 {
				maxScroll = 0
			}
		}
	}
	// Pad body to exactly bodyHeight lines manually. On Windows conhost,
	// lipgloss.Height() can miscount ANSI-wrapped lines, producing off-by-one
	// padding that causes the status bar to overflow and leave residual lines.
	for len(lines) < bodyHeight {
		lines = append(lines, "")
	}
	if len(lines) > bodyHeight {
		lines = lines[:bodyHeight]
	}
	// Visual scrollbar: draw │ (track) and ▐ (thumb) on the right edge
	if needScrollbar && bodyHeight > 0 {
		thumbLine := int(float64(scrollOff) / float64(maxScroll) * float64(bodyHeight-1))
		sbTrack := ScrollbarTrackStyle.Render("│")
		sbThumb := ScrollbarThumbStyle.Render("▐")
		for i := range lines {
			w := lipgloss.Width(lines[i])
			if w < scrollContentWidth {
				lines[i] += strings.Repeat(" ", scrollContentWidth-w)
			}
			if strings.TrimSpace(lines[i]) == "" {
				lines[i] += " "
			} else if i == thumbLine {
				lines[i] += sbThumb
			} else {
				lines[i] += sbTrack
			}
		}
	}
	// Join body lines with newlines. No lipgloss Width wrapping here —
	// renderBody already produces lines within terminal width. Re-wrapping
	// with lipgloss on ANSI-rich lines can silently create extra visual
	// lines, pushing the input box below the visible terminal area.
	//
	// APPEND ANSI RESET before the input box to prevent ANSI escape codes
	// from the last body line from leaking into the input box border rendering.
	// The reset is placed at the START of the input line (not the END of body)
	// because on Windows ConPTY, a trailing ANSI reset on the previous line
	// may not take effect before the next line renders, letting colors bleed
	// into border characters and making them invisible.
	body := strings.Join(lines, "\n")

	var full string
	switch {
	case suggestionPopup != "" && optionsPopup != "":
		full = body + "\n\033[0m" + optionsPopup + "\n\033[0m" + suggestionPopup + "\n\033[0m" + inputLine + "\n\033[0m" + statusLine
	case suggestionPopup != "":
		full = body + "\n\033[0m" + suggestionPopup + "\n\033[0m" + inputLine + "\n\033[0m" + statusLine
	case optionsPopup != "":
		full = body + "\n\033[0m" + optionsPopup + "\n\033[0m" + inputLine + "\n\033[0m" + statusLine
	default:
		full = body + "\n\033[0m" + inputLine + "\n\033[0m" + statusLine
	}
	return full
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ---- Terminal escape split bracket tracker ----
	// On Windows 10, SGR mouse sequences [<65;25;31M can arrive split at buffer
	// boundaries: '[' alone, then '<65;25;31M'. A lone '[' cannot be distinguished
	// from user input, so we hold it: if the next message completes an escape
	// sequence, discard both; otherwise reinject '[' and process as normal input.
	if m.pendingOpenBracket {
		m.pendingOpenBracket = false
		if msg.Type == tea.KeyRunes {
			allRunes := append([]rune{'['}, msg.Runes...)
			if isTerminalEscapeResidue(allRunes) {
				// Set afterResidue so subsequent SGR fragments (e.g. ";25",
				// "65") from further PTY buffer splits are also discarded.
				m.afterResidue = true
				return m, nil // discard both ['['] + ['<65;25;31M']
			}
			// Not escape residue and msg is KeyRunes: reinject the held '['
			// and process the current runes as normal input.
			m.inputBuf.insertRunes([]rune{'['})
			m.inputBuf.HandleKey(msg)
			return m, nil
		}
		// Non-Runes message after a lone '[': the '[' was almost certainly
		// part of an escape sequence (e.g., on Windows where SGR mouse events
		// arrive as MouseMsg instead of KeyRunes). Discard the held '[' and
		// fall through to normal handling of the current message.
		// This prevents '[' from being injected into the input buffer when
		// rapid mouse wheel events produce ghost '[' characters.
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == '[' {
		m.pendingOpenBracket = true
		return m, nil
	}

	// ---- OSC close bracket tracker ----
	// Terminal OSC sequences start with ESC ]. Bubble Tea consumes ESC,
	// leaving ']' as visible residue. A lone ']' may be followed by digits
	// and ';' (OSC params). Hold the ']' and check the next batch.
	if m.pendingCloseBracket {
		m.pendingCloseBracket = false
		if msg.Type == tea.KeyRunes {
			if isOSCContinuation(msg.Runes) {
				// Set afterResidue so the trailing ST backslash (ESC \)
				// that closes the OSC sequence is also discarded.
				m.afterResidue = true
				return m, nil // discard both [']'] + ['11;rgb:...']
			}
			// Not OSC — reinject ']' and process the current runes.
			m.inputBuf.insertRunes([]rune{']'})
			m.inputBuf.HandleKey(msg)
			return m, nil
		}
		// Non-Runes after ']': the ']' was escape residue, discard it.
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == ']' {
		m.pendingCloseBracket = true
		return m, nil
	}

	// ---- Global hotkeys ----

	// Ctrl+Q: quit
	if msg.Type == tea.KeyCtrlQ {
		return m, tea.Quit
	}

	// Esc: cancel if running; in ready state, mark as potential Alt+Enter
	// prefix (macOS terminals send ESC byte before Enter when Option is held).
	if msg.Type == tea.KeyEsc {
		if m.state == stateRunning {
			if m.engine != nil {
				m.engine.Cancel()
			}
			m.state = stateReady
			m.cancelled = true
			m.spinners = nil
			m.toolTree = nil
			m.streaming = ""
			m.pendingEsc = false
			m.afterResidue = false
			m.messages = append(m.messages, DisplayMessage{Role: "system", Content: "已中断"})
		} else if m.state == stateReady {
			if m.showSuggestions {
				// Dismiss suggestions first, then set pendingEsc as fallback
				m.showSuggestions = false
				m.suggestions = nil
				return m, nil
			}
			// ESC byte may be the first half of Option+Enter on macOS terminals.
			// Set a flag so that a subsequent KeyEnter is treated as Alt+Enter.
			m.pendingEsc = true
		}
		return m, nil
	}

	// ---- Escape residue context tracking (before state gating) ----
	// On Mac, terminal DCS/OSC sequences end with ST (ESC \). Bubble Tea
	// consumes the ESC but the \ leaks through as visible characters.
	// Track residue batches and discard trailing backslash terminators.
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		if m.afterResidue {
			if isAllBackslash(msg.Runes) {
				// These backslashes are the ST (String Terminator) bytes
				// from a DCS/OSC sequence whose ESC was consumed by BT.
				m.afterResidue = false
				return m, nil
			}
			if isSGRContinuation(string(msg.Runes)) {
				// Still inside an SGR escape sequence that fragmented
				// across PTY buffer boundaries. Keep afterResidue true
				// and discard digits/semicolons (e.g. ";25", "65").
				return m, nil
			}
			m.afterResidue = false
		}
		if isTerminalEscapeResidue(msg.Runes) {
			m.afterResidue = true
			return m, nil
		}
	}

	// ---- API key prompt ----
	if m.state == stateApiKeyPrompt {
		return m.handleApiKeyInput(msg)
	}

	// ---- Init: block input ----
	if m.state == stateInit {
		return m, nil
	}

	// ---- Running: only allow scroll keys (Ctrl+C/Esc handled above) ----
	if m.state == stateRunning {
		switch msg.Type {
		case tea.KeyPgUp:
			return m.scrollUp(), nil
		case tea.KeyPgDown:
			return m.scrollDown(), nil
		}
		return m, nil
	}

	// ---- Scroll history keyboard shortcuts (stateReady) ----
	if m.state == stateReady {
		switch msg.Type {
		case tea.KeyPgUp:
			return m.scrollUp(), nil
		case tea.KeyPgDown:
			return m.scrollDown(), nil
		}
	}

	// ---- Alt+Enter sequence detection: pendingEsc + Enter = insert newline ----
	if m.pendingEsc {
		m.pendingEsc = false
		if msg.Type == tea.KeyEnter {
			m.inputBuf.insertAtCursor('\n')
			return m, nil
		}
		// Not Enter — clear flag and fall through to normal handling
	}

	// ---- Options keyboard handling (Enter/Tab/Up/Down when visible) ----
	if len(m.activeOptions) > 0 {
		switch msg.Type {
		case tea.KeyTab, tea.KeyEnter:
			// Select the highlighted option: type its number into the input
			m.inputBuf.SetValue(fmt.Sprintf("%d", m.selectedOption+1))
			m.activeOptions = nil
			return m, nil
		case tea.KeyUp:
			m.selectedOption--
			if m.selectedOption < 0 {
				m.selectedOption = len(m.activeOptions) - 1
			}
			return m, nil
		case tea.KeyDown:
			m.selectedOption = (m.selectedOption + 1) % len(m.activeOptions)
			return m, nil
		}
	}

	// ---- Suggestion keyboard handling (Tab/Up/Down when visible) ----
	if m.showSuggestions && len(m.suggestions) > 0 {
		switch msg.Type {
		case tea.KeyTab, tea.KeyEnter:
			// Autocomplete the selected suggestion
			sel := m.suggestions[m.selectedSuggestion]
			m.inputBuf.SetValue(sel.Command + " ")
			m.showSuggestions = false
			m.suggestions = nil
			return m, nil
		case tea.KeyUp:
			m.selectedSuggestion--
			if m.selectedSuggestion < 0 {
				m.selectedSuggestion = len(m.suggestions) - 1
			}
			return m, nil
		case tea.KeyDown:
			m.selectedSuggestion = (m.selectedSuggestion + 1) % len(m.suggestions)
			return m, nil
		}
	}

	// ---- Normal input handling ----
	action := m.inputBuf.HandleKey(msg)

	switch action {
	case ActionSubmit:
		m.showSuggestions = false
		m.activeOptions = nil
		return m.submitInput()
	case ActionQuit:
		return m, tea.Quit
	default:
		// Update slash command suggestions after any input change
		m.updateSuggestions()
		// Dismiss options popup when user types (instead of using keyboard nav)
		if len(m.activeOptions) > 0 && action != ActionNone && action != ActionCursorLeft && action != ActionCursorRight {
			m.activeOptions = nil
		}
		return m, nil
	}
}

// SetSkillSuggestions provides external skill entries (name + description) for
// display as /-prefixed suggestions in the input box.
func (m *Model) SetSkillSuggestions(skills []Suggestion) {
	m.skillSuggestions = skills
}

// updateSuggestions checks the current input and shows/hides slash command suggestions.
func (m *Model) updateSuggestions() {
	val := m.inputBuf.Value()
	// Only show suggestions when input starts with "/"
	if !strings.HasPrefix(val, "/") {
		m.showSuggestions = false
		m.suggestions = nil
		m.selectedSuggestion = 0
		return
	}

	// Don't show suggestions if there's a space after the command (user already typed args)
	if strings.Contains(val, " ") {
		m.showSuggestions = false
		m.suggestions = nil
		m.selectedSuggestion = 0
		return
	}

	// Filter matching commands
	prefix := strings.ToLower(val)
	var matches []Suggestion
	for _, cmd := range slashCommands {
		if strings.HasPrefix(strings.ToLower(cmd.Command), prefix) {
			matches = append(matches, cmd)
		}
	}
	// Also match external skill names as /-shortcuts
	for _, skill := range m.skillSuggestions {
		short := "/" + skill.Command
		if strings.HasPrefix(short, prefix) {
			matches = append(matches, Suggestion{
				Command:     "/" + skill.Command,
				Description: skill.Description,
			})
		}
	}

	if len(matches) > 0 {
		m.suggestions = matches
		m.showSuggestions = true
		if m.selectedSuggestion >= len(matches) {
			m.selectedSuggestion = 0
		}
	} else {
		m.showSuggestions = false
		m.suggestions = nil
		m.selectedSuggestion = 0
	}
}

func (m Model) handleApiKeyInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		key := strings.TrimSpace(m.apiKeyInput)
		if key == "" {
			m.messages = append(m.messages, DisplayMessage{Role: "system", Content: "API key cannot be empty."})
			return m, nil
		}
		return m, func() tea.Msg { return ApiKeySetMsg{Key: key} }
	case tea.KeyBackspace:
		if len(m.apiKeyInput) > 0 {
			m.apiKeyInput = m.apiKeyInput[:len(m.apiKeyInput)-1]
		}
		return m, nil
	case tea.KeyRunes:
		runes := msg.Runes
		// Same escape residue filtering as InputBuffer.HandleKey
		if isTerminalEscapeResidue(runes) {
			return m, nil
		}
		filtered := make([]rune, 0, len(runes))
		for _, r := range runes {
			if !isLikelyControlOrphan(r) {
				filtered = append(filtered, r)
			}
		}
		m.apiKeyInput += string(filtered)
		return m, nil
	}
	return m, nil
}

func (m Model) handleTick() (tea.Model, tea.Cmd) {
	if m.state == stateInit {
		if m.engine == nil {
			m.state = stateApiKeyPrompt
		} else {
			m.state = stateReady
		}
		return m, tea.Tick(spinnerRate, func(time.Time) tea.Msg { return TickMsg{} })
	}

	if m.state == stateRunning || len(m.spinners) > 0 {
		for i := range m.spinners {
			if m.spinners[i].Active {
				m.spinners[i].FrameIdx = (m.spinners[i].FrameIdx + 1) % len(spinnerFrames)
			}
		}
		return m, tea.Tick(spinnerRate, func(time.Time) tea.Msg { return TickMsg{} })
	}
	return m, nil
}

func (m Model) submitInput() (tea.Model, tea.Cmd) {
	if strings.TrimSpace(m.inputBuf.Value()) == "" {
		return m, nil
	}
	if m.state == stateRunning {
		return m, nil
	}
	content := m.inputBuf.Value()
	m.inputBuf.SetValue("")
	m.cancelled = false
	m.messages = append(m.messages, DisplayMessage{Role: "user", Content: content})
	m.toolTree = nil
	m.spinners = nil
	m.streaming = ""

	// Handle local slash commands without invoking the engine
	if strings.TrimSpace(content) == "/help" {
		m.state = stateReady
		m.messages = append(m.messages, DisplayMessage{Role: "assistant", Content: buildHelpText(slashCommands)})
		return m, nil
	}

	if m.engine == nil {
		m.messages = append(m.messages, DisplayMessage{Role: "system", Content: "API key required. Restart and enter key."})
		return m, nil
	}
	m.state = stateRunning
	m.pendingEsc = false
	m.afterResidue = false
	m.spinners = []AgentSpinner{{Role: "deepact", Goal: "processing your request...", Active: true}}
	return m, tea.Batch(
		m.engine.Run(content),
		tea.Tick(spinnerRate, func(time.Time) tea.Msg { return TickMsg{} }),
		waitForProgress(m.progressChan),
	)
}

func (m *Model) finishStreaming(msg EngineResponseMsg) {
	if len(m.toolTree) > 0 {
		m.messages = append(m.messages, DisplayMessage{Role: "toolsummary", Content: renderToolSummary(m.toolTree)})
		m.toolTree = nil
	}
	m.spinners = nil
	if msg.Err != nil {
		runnerLog.Printf("finishStreaming err: %v", msg.Err)
		// Don't show expected cancellation/timeout errors to the user
		errStr := msg.Err.Error()
		if !strings.Contains(errStr, "context canceled") &&
			!strings.Contains(errStr, "context deadline exceeded") &&
			!strings.Contains(errStr, "connection reset") {
			m.messages = append(m.messages, DisplayMessage{Role: "system", Content: msg.Err.Error()})
		}
		m.streaming = ""
		return
	}
	if msg.Response == nil {
		m.messages = append(m.messages, DisplayMessage{Role: "system", Content: "no response"})
		m.streaming = ""
		return
	}
	if msg.Response.Blocked {
		content := ""
		if msg.Response.Summary != "" {
			content = msg.Response.Summary
		}
		if len(msg.Response.Questions) > 0 {
			if content != "" {
				content += "\n\n"
			}
			content += strings.Join(msg.Response.Questions, "\n")
		}
		if len(msg.Response.Options) > 0 {
			// Store options on model for popup rendering (like slash suggestions)
			m.activeOptions = msg.Response.Options
			m.selectedOption = 0
		}
		m.messages = append(m.messages, DisplayMessage{Role: "assistant", Content: content})
		m.streaming = ""
		return
	}
	if msg.Response.Summary != "" {
		m.messages = append(m.messages, DisplayMessage{Role: "assistant", Content: msg.Response.Summary})
		// Clear streaming — the summary contains the complete response text.
		// Partial stream_delta content from sub-agents is already reflected in Summary.
		m.streaming = ""
	} else if m.streaming != "" {
		m.messages = append(m.messages, DisplayMessage{Role: "assistant", Content: m.streaming})
		m.streaming = ""
	}
	if msg.Response.NextStep != "" {
		m.messages = append(m.messages, DisplayMessage{Role: "assistant", Content: msg.Response.NextStep})
	}
}

func (m Model) renderBody(width int) []string {
	lines := []string{}
	if m.state == stateInit {
		lines = append(lines, renderLogoBox(width))
		return strings.Split(lines[0], "\n")
	}

	lines = append(lines, strings.Split(renderLogoBox(width), "\n")...)
	lines = append(lines, "")
	for _, msg := range m.messages {
		lines = append(lines, renderMessage(msg, width)...)
		lines = append(lines, "")
	}
	if m.state == stateApiKeyPrompt {
		lines = append(lines, "┌──────────────────────────────────────────────┐")
		lines = append(lines, "│  Welcome to DeepAct!                        │")
		lines = append(lines, "│  🔑 DeepSeek API Key 需要配置才能使用。      │")
		lines = append(lines, "│  获取地址: https://platform.deepseek.com     │")
		lines = append(lines, "│                                              │")
		lines = append(lines, "│  输入你的 API Key 后按 Enter 确认           │")
		lines = append(lines, "└──────────────────────────────────────────────┘")
		lines = append(lines, "")
		lines = append(lines, "  "+InputPromptStyle.Render("API Key > ")+strings.Repeat("*", len(m.apiKeyInput))+"█")
		return lines
	}
	if len(m.toolTree) > 0 {
		lines = append(lines, renderToolTree(m.toolTree, width)...)
	}
	if m.streaming != "" {
		lines = append(lines, renderStreaming(m.streaming, width)...)
	}
	if len(m.spinners) > 0 {
		lines = append(lines, renderSpinners(m.spinners, width)...)
	}
	return lines
}

func renderLogoBox(width int) string {
	logoLines := []string{
		"   ____                  _        _             ",
		"  |  _ \\  ___  ___ _ __ / \\   ___| |_          ",
		"  | | | |/ _ \\/ _ \\ '_ / _ \\ / __| __|         ",
		"  | |_| |  __/  __/ |_/ ___ \\ (__| |_          ",
		"  |____/ \\___|\\___| .__/_/  \\_\\___|\\__|         ",
		"                  |_|       v0.1.0              ",
		"                                                  ",
		"  Model: deepseek-v4-flash | Type /help          ",
	}
	boxed := boxWithBorder(logoLines, width)
	return LogoStyle.Render(boxed)
}

func boxWithBorder(lines []string, width int) string {
	maxLine := 0
	for _, line := range lines {
		if len(line) > maxLine {
			maxLine = len(line)
		}
	}
	innerWidth := maxLine
	if width > 4 && width-4 < innerWidth {
		innerWidth = width - 4
	}
	border := "╔" + strings.Repeat("═", innerWidth+2) + "╗"
	bottom := "╚" + strings.Repeat("═", innerWidth+2) + "╝"
	rows := []string{border}
	for _, line := range lines {
		trimmed := line
		if len(trimmed) > innerWidth {
			trimmed = trimmed[:innerWidth]
		}
		padded := trimmed + strings.Repeat(" ", innerWidth-len(trimmed))
		rows = append(rows, "║ "+padded+" ║")
	}
	rows = append(rows, bottom)
	return strings.Join(rows, "\n")
}

func renderMessage(msg DisplayMessage, width int) []string {
	content := msg.Content
	switch msg.Role {
	case "user":
		return wrapText(UserMsgStyle.Render("> ")+content, width)
	case "system":
		return wrapText(SystemMsgStyle.Render(content), width)
	case "toolsummary":
		lines := strings.Split(content, "\n")
		styled := make([]string, len(lines))
		for i, line := range lines {
			if strings.Contains(line, "\033[") {
				// Line already has ANSI color codes, render as-is
				styled[i] = line
			} else {
				styled[i] = ToolTreeStyle.Render(line)
			}
		}
		return wrapLines(styled, width)
	default:
		rendered := renderMarkdown(content, width)
		return strings.Split(rendered, "\n")
	}
}

var (
	mdRenderer      *glamour.TermRenderer
	mdRendererWidth int
	mdRendererMu    sync.Mutex
)

func getMarkdownRenderer(width int) *glamour.TermRenderer {
	mdRendererMu.Lock()
	defer mdRendererMu.Unlock()
	if mdRenderer != nil && mdRendererWidth == width {
		return mdRenderer
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width-2),
	)
	if err == nil {
		mdRenderer = r
		mdRendererWidth = width
	}
	return mdRenderer
}

func renderMarkdown(content string, width int) string {
	if width <= 0 {
		width = 80
	}
	r := getMarkdownRenderer(width)
	if r == nil {
		return content
	}
	out, err := r.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimRight(out, "\n")
}

func toolIcon(name string) string {
	switch name {
	case "edit", "write":
		return "📝"
	case "bash":
		return "💻"
	case "read":
		return "📖"
	case "grep", "glob":
		return "🔍"
	default:
		return "⚙"
	}
}

// parseDiffHunks parses a unified diff into hunk-level children.
// Each child has Detail = hunk header, DetailFull = full hunk content.
func parseDiffHunks(fullDetail string) []ToolNode {
	if !isDiffContent(fullDetail) {
		return nil
	}
	_, diff, _ := splitDiff(fullDetail)
	if diff == "" {
		return nil
	}

	var children []ToolNode
	var current ToolNode
	var hunkLines []string
	inHunk := false

	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") && strings.Contains(line, "@@") {
			if inHunk {
				current.DetailFull = strings.TrimRight(strings.Join(hunkLines, "\n"), "\n")
				children = append(children, current)
			}
			current = ToolNode{Name: "hunk", Detail: line}
			hunkLines = []string{line}
			inHunk = true
		} else if inHunk {
			hunkLines = append(hunkLines, line)
		}
	}
	if inHunk {
		current.DetailFull = strings.TrimRight(strings.Join(hunkLines, "\n"), "\n")
		children = append(children, current)
	}
	return children
}

// parseOutputLines splits multi-line output into child nodes (skip summary line).
func parseOutputLines(fullDetail string, maxLines int) []ToolNode {
	if fullDetail == "" {
		return nil
	}
	lines := strings.Split(fullDetail, "\n")
	if len(lines) <= 2 {
		return nil // only summary line or empty
	}

	var children []ToolNode
	for i := 1; i < len(lines) && len(children) < maxLines; i++ {
		if lines[i] == "" && i < len(lines)-1 {
			continue
		}
		children = append(children, ToolNode{Name: "output", Detail: lines[i]})
	}

	if len(lines)-1 > maxLines {
		children = append(children, ToolNode{Name: "output", Detail: fmt.Sprintf("… and %d more lines", len(lines)-1-maxLines)})
	}
	return children
}

func renderToolTree(toolTree []ToolNode, width int) []string {
	lines := []string{}
	if len(toolTree) == 0 {
		return lines
	}
	lines = append(lines, ToolTreeStyle.Render("● Executing..."))
	for i, node := range toolTree {
		conn := "├─"
		if i == len(toolTree)-1 {
			conn = "└─"
		}
		icon := node.Icon
		if icon == "" {
			icon = toolIcon(node.Name)
		}
		status := ""
		if node.Done {
			status = " ✓"
		}
		line := fmt.Sprintf("  %s %s %s%s", conn, icon, node.Detail, status)
		lines = append(lines, ToolTreeStyle.Render(line))
		// Render children (only visible after tool_done)
		for j, child := range node.Children {
			childConn := "│  ├─"
			lastChild := j == len(node.Children)-1
			if lastChild {
				if i == len(toolTree)-1 {
					childConn = "   └─"
				} else {
					childConn = "│  └─"
				}
			} else if i != len(toolTree)-1 {
				childConn = "│  ├─"
			} else {
				childConn = "   ├─"
			}
			// Diff hunk: show full colored content like renderToolSummary
			if node.Done && (node.Name == "edit" || node.Name == "write") {
				hunkContent := child.DetailFull
				if hunkContent != "" {
					rendered := renderDiffHunk(hunkContent, childConn, lastChild, i, len(toolTree))
					for _, hl := range strings.Split(rendered, "\n") {
						if hl != "" {
							lines = append(lines, hl)
						}
					}
					continue
				}
			}
			childLine := fmt.Sprintf("  %s %s", childConn, child.Detail)
			if len(child.Detail) > width-10 {
				childLine = fmt.Sprintf("  %s %s", childConn, child.Detail[:width-13]+"...")
			}
			lines = append(lines, ToolTreeStyle.Render(childLine))
		}
	}
	return wrapLines(lines, width)
}

func renderToolSummary(toolTree []ToolNode) string {
	var b strings.Builder
	// Count modified files
	modified := 0
	for _, n := range toolTree {
		if n.Done && (n.Name == "edit" || n.Name == "write") && len(n.Children) > 0 {
			modified++
		}
	}
	b.WriteString(fmt.Sprintf("● 执行完成 (%d tools, %d files modified):\n", len(toolTree), modified))
	for i, node := range toolTree {
		conn := "├─"
		if i == len(toolTree)-1 {
			conn = "└─"
		}
		icon := node.Icon
		if icon == "" {
			icon = toolIcon(node.Name)
		}
		b.WriteString(fmt.Sprintf("  %s %s %s\n", conn, icon, node.Detail))
		// Render children
		for j, child := range node.Children {
			childConn := "│  ├─"
			lastChild := j == len(node.Children)-1
			if lastChild {
				if i == len(toolTree)-1 {
					childConn = "   └─"
				} else {
					childConn = "│  └─"
				}
			} else if i != len(toolTree)-1 {
				childConn = "│  ├─"
			} else {
				childConn = "   ├─"
			}
			// Diff hunk: show header line then GitHub-style colored content
			if node.Name == "edit" || node.Name == "write" {
				// Parse the full hunk to render with color
				hunkContent := child.DetailFull
				if hunkContent != "" {
					b.WriteString(renderDiffHunk(hunkContent, childConn, lastChild, i, len(toolTree)))
				}
			} else {
				// Plain output line
				b.WriteString(fmt.Sprintf("  %s %s\n", childConn, child.Detail))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// diff styles cached for performance
var (
	diffDeleteStyle     lipgloss.Style
	diffInsertStyle     lipgloss.Style
	diffContextStyle    lipgloss.Style
	diffHunkHeaderStyle lipgloss.Style
	diffLineNumStyle    lipgloss.Style
	diffStylesOnce      sync.Once
)

func initDiffStyles() {
	diffDeleteStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("210")) // light red text, no background
	diffInsertStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("114")) // light green text, no background
	diffContextStyle = lipgloss.NewStyle()
	diffHunkHeaderStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("178")) // yellow
	diffLineNumStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")) // dim gray
}

// renderDiffHunk renders a unified diff hunk with GitHub-style line numbers and background colors.
func renderDiffHunk(hunkContent, conn string, lastChild bool, nodeIdx, totalNodes int) string {
	diffStylesOnce.Do(initDiffStyles)

	lines := strings.Split(hunkContent, "\n")
	if len(lines) == 0 {
		return ""
	}

	var buf strings.Builder
	// Track old/new line numbers from @@ header
	oldNum, newNum := 1, 1

	for k, hl := range lines {
		if hl == "" {
			continue
		}
		hunkConn := conn
		if k > 0 {
			if lastChild && nodeIdx == totalNodes-1 {
				hunkConn = "      "
			} else if lastChild {
				hunkConn = "│    "
			} else {
				hunkConn = "│  │ "
			}
		}

		// Parse @@ header to get starting line numbers
		if strings.HasPrefix(hl, "@@") {
			// Format: @@ -oldStart,oldCount +newStart,newCount @@
			if parts := strings.Split(hl, " "); len(parts) >= 4 {
				oldPart := strings.TrimPrefix(parts[1], "-")
				newPart := strings.TrimPrefix(parts[2], "+")
				oldStartStr := oldPart
				newStartStr := newPart
				if idx := strings.Index(oldPart, ","); idx > 0 {
					oldStartStr = oldPart[:idx]
				}
				if idx := strings.Index(newPart, ","); idx > 0 {
					newStartStr = newPart[:idx]
				}
				if o, err := fmt.Sscanf(oldStartStr, "%d", &oldNum); err == nil && o == 1 {
					// parsed oldNum
				}
				if n, err := fmt.Sscanf(newStartStr, "%d", &newNum); err == nil && n == 1 {
					// parsed newNum
				}
			}
			// Render @@ header
			buf.WriteString("  " + hunkConn + " " + diffHunkHeaderStyle.Render(hl) + "\n")
			continue
		}

		// Determine line type and render with line numbers
		prefix := hl[0:1]
		content := hl[1:] // rest after +/-/space

		switch prefix {
		case "-":
			oldStr := fmt.Sprintf("%d", oldNum)
			newStr := ""
			lineNumStr := fmt.Sprintf("%s%s", leftPad(oldStr, 4), leftPad(newStr, 5))
			buf.WriteString("  " + hunkConn + diffLineNumStyle.Render(lineNumStr) + "│" + diffDeleteStyle.Render(prefix+content) + "\n")
			oldNum++
		case "+":
			oldStr := ""
			newStr := fmt.Sprintf("%d", newNum)
			lineNumStr := fmt.Sprintf("%s%s", leftPad(oldStr, 4), leftPad(newStr, 5))
			buf.WriteString("  " + hunkConn + diffLineNumStyle.Render(lineNumStr) + "│" + diffInsertStyle.Render(prefix+content) + "\n")
			newNum++
		default:
			// context line (space prefix)
			oldStr := fmt.Sprintf("%d", oldNum)
			newStr := fmt.Sprintf("%d", newNum)
			lineNumStr := fmt.Sprintf("%s%s", leftPad(oldStr, 4), leftPad(newStr, 5))
			buf.WriteString("  " + hunkConn + diffLineNumStyle.Render(lineNumStr) + "│ " + content + "\n")
			oldNum++
			newNum++
		}
	}
	return buf.String()
}

// leftPad pads s to width characters by adding spaces on the left.
func leftPad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat(" ", width-len(s)) + s
}

// extractDiffBody extracts the unified diff portion from a tool digest string.
func extractDiffBody(digest string) string {
	if !isDiffContent(digest) {
		return ""
	}
	_, diff, _ := splitDiff(digest)
	return diff
}

// formatDiffLine applies terminal color styling to a single diff line.
func formatDiffLine(line string) string {
	if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
		return "\033[90m" + line + "\033[0m" // dim/gray
	}
	if strings.HasPrefix(line, "@@") && strings.HasSuffix(line, "@@") {
		return "\033[38;5;178m" + line + "\033[0m" // yellow
	}
	if strings.HasPrefix(line, "-") {
		return "\033[31m" + line + "\033[0m" // red
	}
	if strings.HasPrefix(line, "+") {
		return "\033[32m" + line + "\033[0m" // green
	}
	return line
}

// isDiffContent checks if a string contains unified diff content.
func isDiffContent(s string) bool {
	return strings.Contains(s, "\n--- a/") && strings.Contains(s, "\n+++ b/")
}

// splitDiff extracts diff content from a multi-line digest.
// Returns (summaryLine, diffContent, hasDiff).
func splitDiff(digest string) (summary string, diff string, hasDiff bool) {
	lines := strings.SplitN(digest, "\n", 2)
	if len(lines) == 0 {
		return "", "", false
	}
	summary = lines[0]
	if len(lines) < 2 {
		return summary, "", false
	}
	diff = lines[1]
	if !strings.HasPrefix(strings.TrimSpace(diff), "--- a/") &&
		!strings.HasPrefix(strings.TrimSpace(diff), "@@") {
		return summary, "", false
	}
	return summary, diff, true
}

func renderStreaming(streaming string, width int) []string {
	if streaming == "" {
		return []string{}
	}
	return wrapText(AssistantMsgStyle.Render(streaming), width)
}

func renderSpinners(spinners []AgentSpinner, width int) []string {
	if len(spinners) == 0 {
		return []string{}
	}
	lines := []string{}
	for _, spinner := range spinners {
		if spinner.Active {
			frame := spinnerFrames[spinner.FrameIdx%len(spinnerFrames)]
			line := fmt.Sprintf("%s %s: %s", frame, spinner.Role, spinner.Goal)
			lines = append(lines, SpinnerStyle.Render(line))
		} else {
			line := fmt.Sprintf("✓ %s: %s", spinner.Role, spinner.Summary)
			lines = append(lines, SpinnerDoneStyle.Render(line))
		}
	}
	return wrapLines(lines, width)
}

const maxPopupItems = 8

// visiblePopupWindow returns a slice of items centered on the selected index,
// clamped to at most maxItems. Returns (start, end) indices.
func visiblePopupWindow(total, selected, maxItems int) (start, end int) {
	if total <= maxItems {
		return 0, total
	}
	half := maxItems / 2
	start = selected - half
	if start < 0 {
		start = 0
	}
	end = start + maxItems
	if end > total {
		end = total
		start = end - maxItems
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

func renderOptionsPopup(m Model, width int) string {
	if len(m.activeOptions) == 0 || m.state != stateReady {
		return ""
	}
	total := len(m.activeOptions)
	start, end := visiblePopupWindow(total, m.selectedOption, maxPopupItems)
	var lines []string
	for i := start; i < end; i++ {
		opt := m.activeOptions[i]
		prefix := fmt.Sprintf("[%d]", i+1)
		line := fmt.Sprintf("%s %s", SuggestionHotkey.Render(prefix), SuggestionItem.Render(opt))
		if i == m.selectedOption {
			line = SuggestionSelected.Render(" " + prefix + " " + opt + " ")
		}
		lines = append(lines, line)
	}
	// Show overflow indicator
	if total > maxPopupItems {
		remain := total - end
		if remain > 0 {
			lines = append(lines, DimStyle.Render(fmt.Sprintf(" … and %d more (scroll ↑↓)", remain)))
		} else if start > 0 {
			lines = append(lines, DimStyle.Render(fmt.Sprintf(" (↑ scroll for %d more)", start)))
		}
	}
	lines = append(lines, DimStyle.Render("Enter/Tab: select  ↑↓: navigate  or type feedback"))
	content := strings.Join(lines, "\n")
	return SuggestionBox.Width(width - 4).Render(content)
}

func renderedHeight(s string) int {
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

func buildHelpText(commands []Suggestion) string {
	var b strings.Builder
	b.WriteString("# DeepAct — CLI Coding Agent\n\n")
	b.WriteString("## Keyboard Shortcuts\n\n")
	b.WriteString("| Key | Function |\n")
	b.WriteString("|-----|----------|\n")
	b.WriteString("| `Ctrl+Q` | Quit |\n")
	b.WriteString("| `Esc` | Cancel running task |\n")
	b.WriteString("| `Enter` | Submit input |\n")
	b.WriteString("| `Tab` | Autocomplete suggestion |\n")
	b.WriteString("| `\u2191/\u2193` | Navigate suggestions |\n")
	b.WriteString("| `Alt+Enter` | Insert newline |\n")
	b.WriteString("| `Shift+drag` | Select text (bypasses mouse scroll) |\n")
	b.WriteString("\nType a natural language request to start.\n")
	return b.String()
}

func renderSuggestions(m Model, width int) string {
	if !m.showSuggestions || len(m.suggestions) == 0 {
		return ""
	}
	if m.state != stateReady {
		return ""
	}

	total := len(m.suggestions)
	start, end := visiblePopupWindow(total, m.selectedSuggestion, maxPopupItems)

	var lines []string
	for i := start; i < end; i++ {
		sug := m.suggestions[i]
		line := fmt.Sprintf(" %s  %s", SuggestionHotkey.Render(sug.Command), SuggestionDesc.Render(sug.Description))
		if i == m.selectedSuggestion {
			line = SuggestionSelected.Render(" "+sug.Command+" ") + " " + SuggestionDesc.Render(sug.Description)
		}
		lines = append(lines, line)
	}

	// Show overflow indicator
	if total > maxPopupItems {
		remain := total - end
		if remain > 0 {
			lines = append(lines, DimStyle.Render(fmt.Sprintf(" … and %d more (scroll ↑↓)", remain)))
		} else if start > 0 {
			lines = append(lines, DimStyle.Render(fmt.Sprintf(" (↑ scroll for %d more)", start)))
		}
	}

	// Add hint line
	hint := DimStyle.Render(" Tab: autocomplete  ↑↓: navigate  Esc: dismiss")
	lines = append(lines, hint)

	content := strings.Join(lines, "\n")
	return SuggestionBox.Width(width - 4).Render(content)
}

func renderInputLine(m Model) string {
	if m.state == stateApiKeyPrompt {
		return InputBoxStyle.Render(InputPromptStyle.Render("Key> ") + strings.Repeat("*", len(m.apiKeyInput)))
	}

	runes := []rune(m.inputBuf.Value())
	cursor := "█"
	if m.state == stateRunning {
		cursor = ""
	}

	var left, right string
	cursorPos := m.inputBuf.Cursor()
	if cursorPos <= len(runes) {
		left = string(runes[:cursorPos])
		right = string(runes[cursorPos:])
	} else {
		left = m.inputBuf.Value()
	}

	content := left + cursor + right

	// Inner content width must match what InputBoxStyle calculates:
	// Width(m.width-4) minus border (2) minus padding (2) = m.width-8
	innerWidth := m.width - 8
	if innerWidth < 20 {
		innerWidth = 20
	}

	wrapped := wrapInputText("> "+content, innerWidth)
	return InputBoxStyle.Width(m.width - 4).Render(wrapped)
}

func wrapInputText(text string, width int) string {
	lines := strings.Split(text, "\n")
	var result []string
	for _, line := range lines {
		runes := []rune(line)
		if len(runes) <= width {
			result = append(result, line)
			continue
		}
		for len(runes) > width {
			result = append(result, string(runes[:width]))
			runes = runes[width:]
		}
		if len(runes) > 0 {
			result = append(result, string(runes))
		}
	}
	return strings.Join(result, "\n")
}

func inputBoxHeight(m Model) int {
	if m.width <= 0 {
		return 3
	}
	// Must match inner content width of InputBoxStyle:
	// Width(m.width-4) minus border (2) minus padding (2) = m.width-8
	innerWidth := m.width - 8
	if innerWidth < 20 {
		innerWidth = 20
	}
	// Must match what renderInputLine produces: "> " + content + cursor "█"
	cursor := "█"
	if m.state == stateRunning {
		cursor = ""
	}
	content := "> " + m.inputBuf.Value() + cursor
	lines := strings.Split(content, "\n")
	totalLines := 0
	for _, line := range lines {
		runes := []rune(line)
		lineCount := (len(runes) + innerWidth - 1) / innerWidth
		totalLines += lineCount
	}
	return totalLines + 2
}

func estimateCost(tokensIn, tokensOut, cacheHit int, modelName string, pricing *engine.PricingConfig) float64 {
	if pricing == nil {
		return 0
	}
	p, ok := pricing.Models[modelName]
	if !ok {
		p = pricing.Default
	}
	// Non-cached input + cached input at separate rate
	nonCachedInput := tokensIn - cacheHit
	if nonCachedInput < 0 {
		nonCachedInput = 0
	}
	inputCost := float64(nonCachedInput)*p.InputPricePerToken + float64(cacheHit)*p.CacheHitInputPricePerToken
	outputCost := float64(tokensOut) * p.OutputPricePerToken
	return inputCost + outputCost
}

func renderStatusBar(status StatusInfo, scrollOffset, scrollMax int, width int) string {
	// Shortcut hint: Shift+drag for native selection (SGR mouse mode bypass)
	shortcutHint := "Shift+drag select | Alt+Enter newline"
	switch runtime.GOOS {
	case "darwin":
		shortcutHint = "Shift+drag select | ⌥+Enter newline"
	}

	// Scroll position indicator
	scrollHint := ""
	if scrollMax > 0 {
		pct := int(float64(scrollOffset) / float64(scrollMax) * 100)
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		scrollHint = fmt.Sprintf(" ↑%d%% | ", pct)
	}

	line := fmt.Sprintf("%s↑%.1fK ↓%.1fK | %s",
		scrollHint,
		float64(status.TokensIn)/1000.0,
		float64(status.TokensOut)/1000.0,
		shortcutHint,
	)
	if width > 0 {
		line = lipgloss.NewStyle().Width(width).Render(line)
	}
	return StatusBarStyle.Render(line)
}

func wrapText(text string, width int) []string {
	if width <= 0 {
		width = 80
	}
	paragraphs := strings.Split(text, "\n")
	var result []string
	for _, para := range paragraphs {
		if para == "" {
			result = append(result, "")
			continue
		}
		wrapped := wrapLine(para, width)
		result = append(result, wrapped...)
	}
	return result
}

func wrapLine(line string, width int) []string {
	if len([]rune(line)) <= width {
		return []string{line}
	}
	runes := []rune(line)
	var lines []string
	for len(runes) > 0 {
		if len(runes) <= width {
			lines = append(lines, string(runes))
			break
		}
		cut := width
		for cut > 0 && runes[cut] != ' ' && runes[cut] != '　' {
			cut--
		}
		if cut == 0 {
			cut = width
		}
		lines = append(lines, string(runes[:cut]))
		runes = runes[cut:]
		if len(runes) > 0 && (runes[0] == ' ' || runes[0] == '　') {
			runes = runes[1:]
		}
	}
	return lines
}

func wrapLines(lines []string, width int) []string {
	if width <= 0 {
		return lines
	}
	result := []string{}
	for _, line := range lines {
		if len(line) <= width {
			result = append(result, line)
			continue
		}
		result = append(result, wrapText(line, width)...)
	}
	return result
}

type EngineFactory func(key string) (EngineRunner, error)

var engineFactory EngineFactory

func SetEngineFactory(factory EngineFactory) {
	engineFactory = factory
}

func waitForProgress(ch chan ProgressMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}
