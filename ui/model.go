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
	"github.com/charmbracelet/x/ansi"

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
	Role     string
	Content  string
	ToolTree []ToolNode // toolsummary only: toolTree snapshot at completion, for click-to-expand
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
	Icon       string     // [~] [>_] [?] [<>]
	Children   []ToolNode // diff hunks for edit/write, output for bash
}

type StatusInfo struct {
	Model          string
	TokensIn       int
	TokensOut      int
	CacheHitTokens int
	Cost           float64
	SessionCost    float64
	AgentStatus    string
	ExtraMessage   string
}

type Suggestion struct {
	Command     string // e.g. "/规划"
	Args        string // e.g. "<需求描述>"
	Description string // e.g. "分析需求，探索代码并制定方案"
}

// MemberStatus tracks a roundtable member's review progress for UI display.
type MemberStatus struct {
	ID      string // member ID e.g. "architect"
	Name    string // display name e.g. "架构师"
	Avatar  string // emoji e.g. "🏗️"
	Status  string // "running", "done", "error"
	Score   int    // 0-100 (valid when done)
	Verdict string // "approve", "conditional", "reject" (valid when done)
}

// TDDStage represents a phase in the TDD (Red-Green-Refactor) workflow.
type TDDStage struct {
	Phase  string // "red" | "red_verify" | "green" | "green_verify" | "refactor"
	Status string // "running" | "done" | "waiting"
	Detail string // human-readable detail shown in status bar
}

// SubAgentStatus tracks a dispatched sub-agent's progress for UI display.
type SubAgentStatus struct {
	ID      string // unique key for tracking (agent type + index)
	Agent   string // agent type e.g. "sub", "critic", "searcher"
	Goal    string // what the sub-agent is dispatched to do
	Status  string // "running", "done", "error"
	Summary string // result summary when done
}

var slashCommands = []Suggestion{
	{Command: "/help", Args: "", Description: "Show this help screen"},
	{Command: "/clear", Args: "", Description: "Reset session state (clear messages and context)"},
	{Command: "/team", Args: "<需求>", Description: "开启多角色团队协作，并行分析需求并生成统一方案"},
}

type Model struct {
	state               AppState
	messages            []DisplayMessage
	inputBuf            *InputBuffer
	status              StatusInfo
	spinners            []AgentSpinner
	toolTree            []ToolNode
	width               int
	height              int
	engine              EngineRunner
	streaming           string
	thinkingContent     string // deprecated: kept for legacy, no longer fed by reasoning_delta
	thinkingActivity    string // current agent activity shown in thinking box (from "thinking" ProgressMsg)
	apiKeyInput         string
	pendingOpenBracket  bool // Windows: lone '[' held to check if it's escape split
	pendingCloseBracket bool // lone ']' held to check if it's OSC escape (ESC ] Ps ; Pt ST)
	afterResidue        bool // Mac: tracks if prev batch was escape residue (for ST terminator \ filtering)
	ready               bool
	progressChan        chan ProgressMsg
	scrollOffset        int
	cancelled           bool
	pricing             engine.PricingConfig
	needsRepaint        bool // forces full Bubble Tea repaint on next frame

	escAt               time.Time // timestamp of last ESC key, for time-windowed Alt+Enter detection

	// Slash command suggestions
	showSuggestions    bool
	suggestions        []Suggestion
	selectedSuggestion int

	// External skill names (loaded from .deepact/skills/) for / suggestions
	skillSuggestions []Suggestion

	// Active options (plan selection / review actions)
	activeOptions  []string
	selectedOption int

	// Per-message render cache (messages are immutable once added)
	msgCache *messageRenderCache

	// Mouse drag selection
	selection         SelectionState
	clipboardFeedback time.Time // timestamp of last clipboard copy for status bar feedback
	clipboardError    string   // last clipboard error message, shown briefly in status bar
	autoScrollDir     int      // auto-scroll direction during drag: -1=up, 0=none, +1=down
	lastMouseX        int      // last mouse X during drag (screen coords, for auto-scroll)
	lastMouseY        int      // last mouse Y during drag (screen coords, for auto-scroll)

	// Roundtable member progress tracking
	memberStatuses []MemberStatus

	// TDD (test-driven-development) phase tracking
	tddStages []TDDStage

	// Sub-agent parallel execution tracking
	subAgents []SubAgentStatus
}

type messageRenderCache struct {
	lines         [][]string
	width         int
	lastMaxScroll int
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
	logoDelay     = 500 * time.Millisecond
	spinnerRate   = 100 * time.Millisecond
	escWindow     = 500 * time.Millisecond // max delay between ESC and Enter to qualify as Alt+Enter
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
		msgCache:     &messageRenderCache{},
	}
}

func (m Model) ProgressChan() chan ProgressMsg {
	return m.progressChan
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tea.SetWindowTitle("DeepACT"),
		tea.Tick(logoDelay, func(time.Time) tea.Msg {
			return TickMsg{}
		}),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Clear transient input flags on non-key events. This prevents stale
	// pendingEsc from persisting across TickMsg or WindowSizeMsg events,
	// which would cause the next Enter to insert a newline instead of submit.
	//
	// afterResidue is NOT cleared here — it must survive non-KeyMsg events
	// pendingEsc and afterResidue must survive non-KeyMsg events (TickMsg,
	// ProgressMsg) that frequently arrive between split key sequences.
	// ESC + Enter for Alt+Enter on macOS can be separated by timer ticks.
	switch msg.(type) {
	case tea.KeyMsg:
		// flags are managed in handleKey
	default:
		// Don't clear pendingEsc or afterResidue here
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		// Handle motion events (drag) — they have Button=MouseButtonNone
		if msg.Action == tea.MouseActionMotion {
			if m.selection.Active {
				sel := m.selection
				// Map against the mouse-down snapshot, not the live view:
				// streaming output may have appended lines and shifted the
				// live layout, which would otherwise move the selection
				// anchor away from what the user is dragging over.
				sel.End = screenToLine(msg.Y, msg.X, sel.Scroll, sel.BodyHeight, len(sel.Plain))
				m.lastMouseX = msg.X
				m.lastMouseY = msg.Y
				// Auto-scroll edge detection
				scrollEdge := 2
				newDir := 0
				if msg.Y < scrollEdge {
					newDir = -1
				} else if msg.Y >= sel.BodyHeight-scrollEdge {
					newDir = 1
				}
				m.selection = sel
				if newDir != 0 && m.autoScrollDir == 0 {
					m.autoScrollDir = newDir
					return m, tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
						return autoScrollTickMsg{}
					})
				}
				m.autoScrollDir = newDir
			}
			// Force a full repaint on selection changes. Bubble Tea's default
			// per-line frame diff mis-repaints rows whose ANSI changed (e.g.
			// diff rows gaining/losing \x1b[7m reverse video) on iTerm2, leaving
			// the row showing a neighbour's text. A full repaint bypasses the
			// incremental diff entirely. (Same mechanism resize uses.)
			return m, m.repaintCmd()
		}
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if m.state == stateReady || m.state == stateRunning {
				m.scrollOffset += m.height / 3
				if ms := m.msgCache.lastMaxScroll; ms > 0 && m.scrollOffset > ms {
					m.scrollOffset = ms
				}
			}
			return m, m.repaintCmd()
		case tea.MouseButtonWheelDown:
			if m.state == stateReady || m.state == stateRunning {
				m.scrollOffset -= m.height / 3
				if m.scrollOffset < 0 {
					m.scrollOffset = 0
				}
			}
			return m, m.repaintCmd()
		case tea.MouseButtonLeft:
			if msg.Action == tea.MouseActionPress {
				// Snapshot the body at mouse-down. Selection indices anchor
				// into this snapshot so highlight and copy stay correct even
				// if streaming output later shifts the live view.
				totalLines, bodyHeight, rendered, plain := m.computeLayoutFull()
				maxScroll := 0
				if totalLines > bodyHeight {
					maxScroll = totalLines - bodyHeight
				}
				scroll := m.scrollOffset
				if scroll > maxScroll {
					scroll = maxScroll
				}
				if scroll < 0 {
					scroll = 0
				}
				pt := screenToLine(msg.Y, msg.X, scroll, bodyHeight, totalLines)
				m.selection = SelectionState{
					Active:     true,
					Done:       false,
					Start:      pt,
					End:        pt,
					Rendered:   rendered,
					Plain:      plain,
					BodyHeight: bodyHeight,
					Scroll:     scroll,
				}
				m.autoScrollDir = 0
				m.lastMouseX = msg.X
				m.lastMouseY = msg.Y
				return m, m.repaintCmd()
			} else if msg.Action == tea.MouseActionRelease {
				if m.selection.Active {
					sel := m.selection
					sel.End = screenToLine(msg.Y, msg.X, sel.Scroll, sel.BodyHeight, len(sel.Plain))
					sel.Active = false
					m.autoScrollDir = 0
					if sel.Start == sel.End {
						m.selection = SelectionState{}
					} else {
						sel.Done = true
						m.selection = sel
						// copySelection uses sel.Plain (the snapshot), so the
						// clipboard receives exactly what was selected at
						// mouse-down, not the live shifted view.
						_, err := copySelection(nil, m.selection)
						if err != nil {
							m.clipboardError = err.Error()
						} else {
							m.clipboardError = ""
						}
						m.clipboardFeedback = time.Now()
					}
				}
				// Force full repaint on release: the selection highlight is
				// either finalized (Done) or cleared, both change row ANSI and
				// can mis-repaint under Bubble Tea's incremental frame diff.
				return m, m.repaintCmd()
			}
		}
		return m, nil
	case autoScrollTickMsg:
		if m.selection.Active && m.autoScrollDir != 0 {
			sel := m.selection
			// Auto-scroll within the snapshot rather than the live view,
			// keeping the selection anchored to the frozen content.
			totalLines := len(sel.Plain)
			maxScroll := 0
			if totalLines > sel.BodyHeight {
				maxScroll = totalLines - sel.BodyHeight
			}
			sel.Scroll -= m.autoScrollDir
			if sel.Scroll < 0 {
				sel.Scroll = 0
			}
			if maxScroll > 0 && sel.Scroll > maxScroll {
				sel.Scroll = maxScroll
			}
			sel.End = screenToLine(m.lastMouseY, m.lastMouseX, sel.Scroll, sel.BodyHeight, totalLines)
			m.selection = sel
			return m, tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
				return autoScrollTickMsg{}
			})
		}
		m.autoScrollDir = 0
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
		m.selection = SelectionState{} // new message: clear selection
		m.autoScrollDir = 0
		if m.cancelled {
			m.cancelled = false
			return m, nil
		}
		m.state = stateReady
		// Only reset scroll if user wasn't reading history
		if m.scrollOffset <= 0 {
			m.scrollOffset = 0
		}
		m.thinkingContent = ""
		m.thinkingActivity = ""
		m.memberStatuses = nil // roundtable phase done, clear member cards
		m.subAgents = nil      // sub-agent panel done, clear
		m.tddStages = nil      // TDD phase done, clear stage cards
		m.finishStreaming(msg)
		return m, m.repaintCmd()
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
			m.thinkingActivity = msg.Detail
		case "retry":
			if len(m.spinners) > 0 {
				m.spinners[0].Goal = msg.Detail
			}
			m.thinkingActivity = msg.Detail
		case "reasoning_delta":
			// No longer fed into thinkingContent — raw LLM reasoning is not useful
			// to display. Agent activity is shown via "thinking" events instead.
			if len(m.spinners) > 0 {
				m.spinners[0].Goal = "thinking..."
			}
		case "member_start":
			m.memberStatuses = append(m.memberStatuses, MemberStatus{
				ID:     msg.Name,
				Name:   msg.Detail,
				Avatar: memberAvatar(msg.Name),
				Status: "running",
			})
		case "member_done":
			for i := range m.memberStatuses {
				if m.memberStatuses[i].ID == msg.Name {
					m.memberStatuses[i].Status = "done"
					if score := extractScore(msg.Detail); score >= 0 {
						m.memberStatuses[i].Score = score
					}
					if strings.Contains(msg.Detail, "✅") {
						m.memberStatuses[i].Verdict = "approve"
					} else if strings.Contains(msg.Detail, "⚠️") {
						m.memberStatuses[i].Verdict = "conditional"
					} else if strings.Contains(msg.Detail, "❌") {
						m.memberStatuses[i].Verdict = "reject"
					}
					break
				}
			}
		case "roundtable_enter":
			m.memberStatuses = nil
			if len(m.spinners) > 0 {
				m.spinners[0].Goal = "🔄 " + msg.Detail
			}
		case "roundtable_phase":
			if msg.Name == "review" {
				m.memberStatuses = nil
			}
			if len(m.spinners) > 0 {
				m.spinners[0].Goal = "🔄 " + msg.Detail
			}
			if msg.Name == "explore_done" || msg.Name == "review_done" {
				m.memberStatuses = nil
			}
		case "agent_start":
			displayName := msg.Name
			goal := msg.Detail
			if len(goal) > 60 {
				goal = goal[:60] + "..."
			}
			m.toolTree = append(m.toolTree, ToolNode{Name: displayName, Detail: goal})
			if len(m.spinners) > 0 {
				m.spinners[0].Goal = displayName + " running..."
			}
			// Track sub-agent in the dedicated panel
			m.subAgents = append(m.subAgents, SubAgentStatus{
				ID:     fmt.Sprintf("%s-%d", displayName, len(m.subAgents)),
				Agent:  displayName,
				Goal:   msg.Detail,
				Status: "running",
			})
		case "agent_done":
			displayName := msg.Name
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
			// Update sub-agent panel status
			for i := range m.subAgents {
				if m.subAgents[i].Agent == displayName && m.subAgents[i].Status == "running" {
					m.subAgents[i].Status = "done"
					m.subAgents[i].Summary = msg.Detail
					break
				}
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
			m.status.CacheHitTokens += msg.CacheHit
			cost := estimateCost(msg.TokensIn, msg.TokensOut, msg.CacheHit, msg.ModelName, &m.pricing)
			m.status.Cost = cost
			m.status.SessionCost += cost
		case "skill_activated":
			m.messages = append(m.messages, DisplayMessage{
				Role:    "system",
				Content: fmt.Sprintf("Skill activated: **%s** — %s", msg.Name, msg.Detail),
			})
		case "tdd_phase":
			// Update or add TDD stage
			found := false
			for i, s := range m.tddStages {
				if s.Phase == msg.Name {
					m.tddStages[i].Status = "running"
					m.tddStages[i].Detail = msg.Detail
					found = true
				} else if s.Status == "running" {
					// Previous stages are now done
					m.tddStages[i].Status = "done"
				}
			}
			if !found {
				// Mark all previous stages as done
				for i := range m.tddStages {
					if m.tddStages[i].Status == "waiting" {
						m.tddStages[i].Status = "done"
					}
				}
				// Add the new stage
				m.tddStages = append(m.tddStages, TDDStage{
					Phase:  msg.Name,
					Status: "running",
					Detail: msg.Detail,
				})
			}
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
	// Repaint guard: if the viewport content changed (scroll, new messages,
	// streaming finished), send a WindowSizeMsg to force Bubble Tea's internal
	// full repaint. This is the same mechanism that "resize terminal" uses to
	// fix rendering artifacts — we trigger it programmatically.
	if m.needsRepaint {
		m.needsRepaint = false
		return m, func() tea.Msg {
			return tea.WindowSizeMsg{Width: m.width, Height: m.height}
		}
	}
	return m, nil
}

// repaintCmd returns a Cmd that fires a WindowSizeMsg with the current
// dimensions, forcing Bubble Tea to do a full frame repaint. This prevents
// visual artifacts (duplicated/ghost content) that occur when scrolling
// or after engine responses, where incremental diff rendering leaves stale
// terminal output.
func (m Model) repaintCmd() tea.Cmd {
	return func() tea.Msg {
		return tea.WindowSizeMsg{Width: m.width, Height: m.height}
	}
}

// scrollUp increases scroll offset by half the terminal height.
func (m Model) scrollUp() Model {
	m.scrollOffset += m.height / 2
	if ms := m.msgCache.lastMaxScroll; ms > 0 && m.scrollOffset > ms {
		m.scrollOffset = ms
	}
	return m
}

// footerHeight computes the footer height the same way View() does: by
// rendering the input line and any popups and summing their rendered row
// counts. This is shared with View() (via computeLayout) so mouse coordinate
// mapping uses the identical body height that was actually displayed — the
// previous formula diverged from View by 1 row whenever a popup showed an
// overflow indicator or was suppressed by state, shifting selection mapping.
func (m Model) footerHeight() int {
	h := 3 + renderedHeight(renderInputLine(m))
	if m.showSuggestions && len(m.suggestions) > 0 {
		h += renderedHeight(renderSuggestions(m, m.width))
	}
	if len(m.activeOptions) > 0 {
		h += renderedHeight(renderOptionsPopup(m, m.width))
	}
	return h
}

// computeLayout returns (totalLines, bodyHeight, plainLines) for the current model state.
// Used by mouse event handlers for coordinate mapping and text extraction.
func (m Model) computeLayout() (totalLines, bodyHeight int, plainLines []string) {
	totalLines, bodyHeight, _, plainLines = m.computeLayoutFull()
	return totalLines, bodyHeight, plainLines
}

// computeLayoutFull returns the live body layout plus the rendered (styled) and
// plain line arrays. Used at mouse-down to snapshot the body so a selection
// stays anchored even while streaming output shifts the live view.
func (m Model) computeLayoutFull() (totalLines, bodyHeight int, rendered, plain []string) {
	bh := m.height - m.footerHeight()
	if bh < 1 {
		bh = 1
	}
	rendered, plain = m.renderBody(m.renderBodyWidth())
	return len(plain), bh, rendered, plain
}

// renderBodyWidth returns the content width used by renderBody.
func (m Model) renderBodyWidth() int {
	w := m.width - 1
	if w < 20 {
		w = 20
	}
	return w
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
		rendered, _ := m.renderBody(contentWidth)
		return strings.Join(rendered, "\n")
	}

	suggestionPopup := renderSuggestions(m, contentWidth)
	optionsPopup := renderOptionsPopup(m, contentWidth)

	// ---- Step 1: Render footer components (bottom-fixed area) ----
	inputLine := renderInputLine(m)

	// ---- Step 2: Compute footer height — this area is FIXED and NEVER scrolls ----
	// Status bar is always 3 lines (top padding, content line, bottom padding)
	footerHeight := 3 + renderedHeight(inputLine)
	if suggestionPopup != "" {
		footerHeight += renderedHeight(suggestionPopup)
	}
	if optionsPopup != "" {
		footerHeight += renderedHeight(optionsPopup)
	}

	// ---- Step 3: Body area = remaining space above footer ----
	bodyHeight := m.height - footerHeight
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	// ---- Step 4: Render body with scroll offset ----
	needScrollbar := false
	scrollContentWidth := contentWidth - 1
	if scrollContentWidth < 20 {
		scrollContentWidth = 20
	}

	var lines []string
	total := 0
	maxScroll := 0
	scrollOff := 0
	frozen := (m.selection.Active || m.selection.Done) && m.selection.Rendered != nil
	if frozen {
		// Selection freeze: display the body snapshot captured at mouse-down
		// so the highlight and copy target stay aligned with what the user
		// clicked, even while streaming output appends lines and shifts the
		// live view. Copy the snapshot first — applySelectionHighlight mutates
		// the slice in place and must not corrupt the stored snapshot.
		sel := m.selection
		lines = append([]string(nil), sel.Rendered...)
		lines = m.applySelectionHighlight(lines)
		total = len(lines)
		scrollOff = sel.Scroll
		if total > bodyHeight {
			needScrollbar = true
			maxScroll = total - bodyHeight
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
	} else {
		renderedLines, _ := m.renderBody(scrollContentWidth)
		renderedLines = m.applySelectionHighlight(renderedLines)
		lines = renderedLines
		total = len(lines)
		scrollOff = m.scrollOffset
		if total > bodyHeight {
			needScrollbar = true
			maxScroll = total - bodyHeight
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
		m.msgCache.lastMaxScroll = maxScroll
	}

	// ---- Step 5: Render status bar with actual scroll info (single pass) ----
	statusLine := renderStatusBar(m.status, scrollOff, maxScroll, contentWidth, m.clipboardFeedback, m.clipboardError)

	// ---- Step 6: Pad/trim body to exactly bodyHeight ----
	if m.state == stateInit || (len(m.messages) == 0 && m.streaming == "" && len(m.spinners) == 0) {
		// Init state or no messages yet: logo is the only content — pad from BOTTOM so logo stays at top
		for len(lines) < bodyHeight {
			lines = append(lines, "")
		}
	} else {
		// Messages exist: pad from BOTTOM so content stays at the top, not pushed
		// to the bottom by blank lines (which looks terrible with short responses).
		for len(lines) < bodyHeight {
			lines = append(lines, "")
		}
	}
	// Trim from TOP if body exceeds allocated height
	if len(lines) > bodyHeight {
		excess := len(lines) - bodyHeight
		lines = lines[excess:]
	}

	// ---- Step 7: Truncate all body lines to terminal width, then pad ----
	// Padding prevents Bubble Tea's incremental frame diff from leaving
	// stale characters from the previous frame in blank positions.
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], contentWidth, "")
		if w := ansi.StringWidth(lines[i]); w < contentWidth {
			lines[i] += strings.Repeat(" ", contentWidth-w)
		}
	}

	// ---- Step 8: Visual scrollbar (removed per user request — was adding │/▐ to right side) ----
	if needScrollbar && bodyHeight > 0 && maxScroll > 0 {
		// Keep scrollbar calculation for maxScroll used in status bar
		// but don't add any characters that pollute copy-paste.
	}

	// ---- Step 9: Assemble final output — BOTTOM-UP, footer pinned ----
	// ANSI reset before footer prevents color bleed from last body line.
	// Order (top to bottom): body → popups → input → status
	body := strings.Join(lines, "\n")

	footerParts := []string{inputLine, statusLine}
	if suggestionPopup != "" {
		footerParts = append([]string{suggestionPopup}, footerParts...)
	}
	if optionsPopup != "" {
		footerParts = append([]string{optionsPopup}, footerParts...)
	}
	footer := "\033[0m" + strings.Join(footerParts, "\n\033[0m")

	full := body + "\n" + footer

	// ---- Step 10: Ensure total = m.height by adding/removing top padding ----
	finalLines := strings.Split(full, "\n")
	if len(finalLines) > m.height {
		// Trim from TOP only (preserve footer at bottom)
		excess := len(finalLines) - m.height
		finalLines = finalLines[excess:]
	} else if len(finalLines) < m.height {
		deficit := m.height - len(finalLines)
		blankLine := strings.Repeat(" ", contentWidth)
		for i := 0; i < deficit; i++ {
			finalLines = append([]string{blankLine}, finalLines...)
		}
	}

	// ---- Step 11: Final width truncation ----
	for i := range finalLines {
		finalLines[i] = ansi.Truncate(finalLines[i], m.width, "")
	}

	return strings.Join(finalLines, "\n")
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Any key press clears the current selection
	if m.selection.Done {
		m.selection = SelectionState{}
	}

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
			m.escAt = time.Time{}
			m.afterResidue = false
			m.messages = append(m.messages, DisplayMessage{Role: "system", Content: "已中断"})
		} else if m.state == stateReady {
			// ESC: clear input buffer, discard current input
			m.inputBuf.SetValue("")
			m.showSuggestions = false
			m.suggestions = nil
			// ESC byte may be the first half of Option+Enter on macOS terminals.
			// Record timestamp; a subsequent Enter within escWindow is treated as Alt+Enter.
			m.escAt = time.Now()
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
			if isOSCColorContinuation(string(msg.Runes)) {
				// Still inside an OSC color response that fragmented
				// across PTY buffer boundaries. Keep afterResidue true
				// and discard hex/slash/colon fragments (e.g. "/fae0/fae0",
				// "0/fae0/fae0\", ":fae0/fae0/fae0\").
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
			return m.scrollUp(), m.repaintCmd()
		case tea.KeyPgDown:
			return m.scrollDown(), m.repaintCmd()
		}
		return m, nil
	}

	// ---- Scroll history keyboard shortcuts (stateReady) ----
	if m.state == stateReady {
		switch msg.Type {
		case tea.KeyPgUp:
			return m.scrollUp(), m.repaintCmd()
		case tea.KeyPgDown:
			return m.scrollDown(), m.repaintCmd()
		}
	}

	// ---- Alt+Enter sequence detection: ESC + Enter within time window = insert newline ----
	// On terminals that send Alt+Enter as two separate events (ESC then Enter),
	// we use a time window to distinguish genuine Alt+Enter from ESC (dismiss)
	// followed by Enter (submit) after a delay.
	if !m.escAt.IsZero() {
		if time.Since(m.escAt) > escWindow {
			m.escAt = time.Time{} // expired, treat as two separate keys
		} else if msg.Type == tea.KeyEnter {
			m.escAt = time.Time{}
			altMsg := msg
			altMsg.Alt = true
			m.inputBuf.HandleKey(altMsg)
			return m, nil
		} else if msg.Type != tea.KeyEsc {
			// Non-Enter, non-ESC key after ESC — not Alt+Enter, clear
			m.escAt = time.Time{}
		}
		// msg.Type == tea.KeyEsc: another ESC, update escAt (handled above)
	}

	// ---- Alt+Enter (single event, e.g. Kitty protocol \x1b[13;3u): insert newline ----
	// This must be checked BEFORE options/suggestions handlers, which would
	// intercept KeyEnter without checking msg.Alt.
	// Clear suggestions since the user explicitly chose to insert a newline
	// rather than autocomplete. Delegate to HandleKey so PasteMode state is
	// handled correctly (insertAtCursor directly would corrupt the indicator).
	if msg.Type == tea.KeyEnter && msg.Alt {
		m.showSuggestions = false
		m.suggestions = nil
		m.inputBuf.HandleKey(msg)
		return m, nil
	}

	// ---- macOS Option key detection (iTerm2 Normal mode): insert newline ----
	// On iTerm2 default config, Option+Enter sends \r without ESC prefix, so
	// msg.Alt is false. We use the macOS HID API (CGEventSourceFlagsState) to
	// detect the physical Option key — zero terminal state modification.
	// Delegate to HandleKey so PasteMode state is handled correctly.
	if runtime.GOOS == "darwin" && msg.Type == tea.KeyEnter && !msg.Alt && optionKeyPressed() {
		m.showSuggestions = false
		m.suggestions = nil
		altMsg := msg
		altMsg.Alt = true
		m.inputBuf.HandleKey(altMsg)
		return m, nil
	}

	// ---- Windows Alt key detection (ConPTY / Windows Terminal): insert newline ----
	// On Windows, ConPTY and most terminals send Alt+Enter as a plain KeyEnter
	// event without the Alt=true modifier. We use GetAsyncKeyState to detect
	// the physical Alt key independently of what the terminal reports.
	// Delegate to HandleKey so PasteMode state is handled correctly.
	if runtime.GOOS == "windows" && msg.Type == tea.KeyEnter && !msg.Alt && optionKeyPressed() {
		m.showSuggestions = false
		m.suggestions = nil
		altMsg := msg
		altMsg.Alt = true
		m.inputBuf.HandleKey(altMsg)
		return m, nil
	}

	// ---- Options keyboard handling (Enter/Tab/Up/Down when visible) ----
	if len(m.activeOptions) > 0 {
		switch msg.Type {
		case tea.KeyTab:
			// Select the highlighted option: type its number into the input
			m.inputBuf.SetValue(fmt.Sprintf("%d", m.selectedOption+1))
			m.activeOptions = nil
			return m, nil
		case tea.KeyEnter:
			if !msg.Alt {
				// Select option on plain Enter
				m.inputBuf.SetValue(fmt.Sprintf("%d", m.selectedOption+1))
				m.activeOptions = nil
				return m, nil
			}
			// Alt+Enter: fall through to InputBuffer for newline
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
		case tea.KeyTab:
			// Autocomplete the selected suggestion
			sel := m.suggestions[m.selectedSuggestion]
			m.inputBuf.SetValue(sel.Command + " ")
			m.showSuggestions = false
			m.suggestions = nil
			return m, nil
		case tea.KeyEnter:
			if !msg.Alt {
				// Autocomplete on plain Enter
				sel := m.suggestions[m.selectedSuggestion]
				m.inputBuf.SetValue(sel.Command + " ")
				m.showSuggestions = false
				m.suggestions = nil
				return m, nil
			}
			// Alt+Enter: fall through to InputBuffer for newline
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
	content := m.inputBuf.SubmitContent()
	if strings.TrimSpace(content) == "" {
		return m, nil
	}
	if m.state == stateRunning {
		return m, nil
	}
	m.inputBuf.SetValue("")
	m.cancelled = false
	m.messages = append(m.messages, DisplayMessage{Role: "user", Content: content})
	m.toolTree = nil
	m.spinners = nil
	m.memberStatuses = nil
	m.subAgents = nil
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
	m.escAt = time.Time{}
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
		// Snapshot toolTree into the message so click-to-expand works after
		// completion (m.toolTree is cleared below). Deep-copy: ToolNode is a
		// value, but Children is a slice that must not alias the live tree.
		snapshot := append([]ToolNode(nil), m.toolTree...)
		for i := range snapshot {
			snapshot[i].Children = append([]ToolNode(nil), snapshot[i].Children...)
		}
		m.messages = append(m.messages, DisplayMessage{
			Role:     "toolsummary",
			Content:  renderToolSummary(m.toolTree),
			ToolTree: snapshot,
		})
		m.toolTree = nil
	}
	m.spinners = nil
	if msg.Err != nil {
		runnerLog.Printf("finishStreaming err: %v", msg.Err)
		errStr := msg.Err.Error()
		// Suppress only expected cancellation/timeout — these are user-initiated
		// and the "已中断" message is already shown via the Esc handler.
		if strings.Contains(errStr, "context canceled") ||
			strings.Contains(errStr, "context deadline exceeded") {
			m.streaming = ""
			return
		}
		m.messages = append(m.messages, DisplayMessage{Role: "system", Content: msg.Err.Error()})
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

func (m Model) renderBody(width int) (rendered []string, plain []string) {
	lines := []string{}

	if m.state == stateInit {
		logoRendered := renderLogoBox(width)
		logoLines := strings.Split(logoRendered, "\n")
		plainLines := make([]string, len(logoLines))
		for i, l := range logoLines {
			plainLines[i] = stripAnsi(l)
		}
		return logoLines, plainLines
	}

	// Only show logo box on the initial welcome screen (before any conversation).
	// Once messages exist, the logo wastes vertical space and leaves a blank area
	// above the actual content (especially during stateRunning padding from TOP).
	if len(m.messages) == 0 {
		logoLines := strings.Split(renderLogoBox(width), "\n")
		lines = append(lines, logoLines...)
		lines = append(lines, "")
	}

	// Use per-message render cache: messages are immutable once added,
	// so only render new messages or on width change.
	cache := m.msgCache
	if cache.width != width {
		cache.lines = nil
		cache.width = width
	}
	for i, msg := range m.messages {
		if i < len(cache.lines) {
			lines = append(lines, cache.lines[i]...)
		} else {
			rendered := renderMessage(msg, width)
			rendered = append(rendered, "")
			cache.lines = append(cache.lines, rendered)
			lines = append(lines, rendered...)
		}
	}

	if m.state == stateApiKeyPrompt {
		apiKeyLines := []string{
			"┌────────────────────────────────────────────┐",
			"│  Welcome to DeepAct!                        │",
			"│  🔑 DeepSeek API Key 需要配置才能使用。      │",
			"│  获取地址: https://platform.deepseek.com     │",
			"│                                              │",
			"│  输入你的 API Key 后按 Enter 确认           │",
			"└────────────────────────────────────────────┘",
			"",
			"  " + InputPromptStyle.Render("API Key > ") + strings.Repeat("*", len(m.apiKeyInput)) + "█",
		}
		lines = append(lines, apiKeyLines...)
		plainLines := make([]string, len(lines))
		for i, l := range lines {
			plainLines[i] = stripAnsi(l)
		}
		return lines, plainLines
	}
	if len(m.toolTree) > 0 {
		toolLines := m.renderToolTree(width)
		lines = append(lines, toolLines...)
	}
	if len(m.subAgents) > 0 {
		// Sub-agent parallel execution panel: shows which sub-agents are
		// dispatched and their current status (running/done).
		subAgentLines := renderSubAgentPanel(m.subAgents, width)
		lines = append(lines, subAgentLines...)
	}
	if len(m.memberStatuses) > 0 || len(m.tddStages) > 0 {
		// Overlay status area: render TDD phases (left) and/or member
		// progress (right) in a single status block above the input.
		overlayLines := renderOverlayStatus(m.tddStages, m.memberStatuses, width)
		lines = append(lines, overlayLines...)
	} else if m.streaming != "" {
		streamLines := renderStreaming(m.streaming, width)
		lines = append(lines, streamLines...)
	}
	if len(m.spinners) > 0 {
		spinnerLines := renderSpinners(m.spinners, width)
		lines = append(lines, spinnerLines...)
	}
	plainLines := make([]string, len(lines))
	for i, l := range lines {
		plainLines[i] = stripAnsi(l)
	}
	return lines, plainLines
}


func renderLogoBox(width int) string {
	// Mascot whale art (user-chosen design) — left side
	mascotLines := []string{
		"           .           ",
		"          \":\"         ",
		"        ___:____     |\"\\/\"|",
		"      ,'        `.    \\  /",
		"      |  O        \\___/  |",
		"    ~^~^~^~^~^~^~^~^~^~^~^~^~",
	}

	// ASCII art logo — right side
	bigLogo := []string{
		"  ██████╗ ███████╗███████╗██████╗  █████╗  ██████╗████████╗",
		"  ██╔══██╗██╔════╝██╔════╝██╔══██╗██╔══██╗██╔════╝╚══██╔══╝",
		"  ██║  ██║█████╗  █████╗  ██████╔╝███████║██║        ██║   ",
		"  ██║  ██║██╔══╝  ██╔══╝  ██╔═══╝ ██╔══██║██║        ██║   ",
		"  ██████╔╝███████╗███████╗██║     ██║  ██║╚██████╗   ██║   ",
		"  ╚═════╝ ╚══════╝╚══════╝╚═╝     ╚═╝  ╚═╝ ╚═════╝   ╚═╝   ",
	}

	// Model name line
	flashLine := FlashModelStyle.Render("  deepseek V4 flash")

	// Style each mascot line: whale body in cyan, blowhole dot in yellow, waves in blue
	styledMascot := make([]string, len(mascotLines))
	for i, line := range mascotLines {
		switch i {
		case 0:
			styledMascot[i] = MascotAccentStyle.Render(line)
		case 5:
			styledMascot[i] = MascotWaveStyle.Render(line)
		default:
			styledMascot[i] = MascotStyle.Render(line)
		}
	}

	// Style big logo lines with gradient
	styledLogo := make([]string, len(bigLogo))
	for i, line := range bigLogo {
		switch {
		case i < 3:
			styledLogo[i] = LogoGradient1.Render(line)
		default:
			styledLogo[i] = LogoGradient2.Render(line)
		}
	}

	// Compute visual widths for alignment
	mascotW := 0
	for _, l := range styledMascot {
		if w := lipgloss.Width(l); w > mascotW {
			mascotW = w
		}
	}
	logoW := 0
	for _, l := range styledLogo {
		if w := lipgloss.Width(l); w > logoW {
			logoW = w
		}
	}

	// Build the right column: 6 lines of big ASCII art
	rightCol := make([]string, 6)
	for i := 0; i < 6; i++ {
		l := styledLogo[i]
		if w := lipgloss.Width(l); w < logoW {
			l += strings.Repeat(" ", logoW-w)
		}
		rightCol[i] = l
	}

	// Join left + right side by side
	combined := make([]string, 6)
	for i := 0; i < 6; i++ {
		left := styledMascot[i]
		if w := lipgloss.Width(left); w < mascotW {
			left += strings.Repeat(" ", mascotW-w)
		}
		combined[i] = left + "  " + rightCol[i]
	}

	// Slogan below the left-right layout
	slogan := SloganStyle.Render("Your AI-powered coding companion")

	allLines := append(combined, "", flashLine, "", slogan)
	boxed := boxWithBorder(allLines, width)
	return LogoStyle.Render(boxed)
}

func boxWithBorder(lines []string, width int) string {
	maxLine := 0
	for _, line := range lines {
		w := lipgloss.Width(line)
		if w > maxLine {
			maxLine = w
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
		w := lipgloss.Width(line)
		trimmed := line
		if w > innerWidth {
			trimmed = ansi.Truncate(line, innerWidth, "")
			w = innerWidth
		}
		padded := trimmed + strings.Repeat(" ", innerWidth-w)
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
				styled[i] = DimStyle.Render(line)
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
	// Use custom style configs instead of glamour.WithStandardStyle() to avoid
	// the │ prefix that glamour's built-in styles add to code blocks, which
	// interferes with diff display and looks noisy.
	// Also avoids WithAutoStyle() because it calls termenv.HasDarkBackground()
	// which sends an OSC 11 terminal query that races with BT's stdin reader
	// (causes garbled "fae0/fae0/fae0" on macOS).
	styleConfig := CustomLightStyle
	if isDark {
		styleConfig = CustomDarkStyle
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(styleConfig),
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
	// Trim both leading and trailing newlines: the glamour Document style has
	// Margin(2) + BlockPrefix("\n") which produces 3 leading newlines. These
	// create a large blank alternating area between toolsummary and assistant
	// content (especially visible in the blocked/"确认执行代码" state).
	return strings.Trim(strings.TrimRight(out, "\n"), "\n")
}

func toolIcon(name string) string {
	switch name {
	case "edit", "write":
		return "[~]"
	case "bash":
		return "[>_]"
	case "read":
		return "[<>]"
	case "grep", "glob":
		return "[?]"
	case "lsp":
		return "[@]"
	case "skill_install":
		return "[+]"
	case "handoff_to_agent":
		return "[→]"
	default:
		return "[*]"
	}
}

// memberAvatar returns a default emoji for known member IDs.
func memberAvatar(id string) string {
	switch id {
	case "architect":
		return "🏗️"
	case "security":
		return "🔒"
	case "quality":
		return "📐"
	case "maintainer":
		return "🔧"
	default:
		return "🧑"
	}
}

// extractScore parses a score value from a progress detail string.
// Expected format: "(评分: 85)" or "(score: 85)"
func extractScore(detail string) int {
	if idx := strings.Index(detail, "评分: "); idx >= 0 {
		rest := detail[idx+len("评分: "):]
		var score int
		if _, err := fmt.Sscanf(rest, "%d", &score); err == nil {
			return score
		}
	}
	if idx := strings.Index(detail, "score: "); idx >= 0 {
		rest := detail[idx+len("score: "):]
		var score int
		if _, err := fmt.Sscanf(rest, "%d", &score); err == nil {
			return score
		}
	}
	return -1
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

// parseOutputLines splits multi-line output into child nodes.
// Unlike the earlier design (which skipped line 0 as a "summary"), bash output
// has no summary line — every line is content. Start from index 0.
func parseOutputLines(fullDetail string, maxLines int) []ToolNode {
	if fullDetail == "" {
		return nil
	}
	lines := strings.Split(fullDetail, "\n")
	if len(lines) <= 1 {
		return nil
	}

	var children []ToolNode
	for i := 0; i < len(lines) && len(children) < maxLines; i++ {
		line := strings.TrimRight(lines[i], "\r")
		if line == "" && i < len(lines)-1 {
			continue
		}
		children = append(children, ToolNode{Name: "output", Detail: line})
	}

	if len(lines) > maxLines {
		children = append(children, ToolNode{Name: "output", Detail: fmt.Sprintf("… and %d more lines", len(lines)-maxLines)})
	}
	return children
}

func (m Model) renderToolTree(width int) []string {
	lines := []string{}
	if len(m.toolTree) == 0 {
		return lines
	}

	blockWidth := width - 2
	if blockWidth < 40 {
		blockWidth = 40
	}

	var searchItems []ToolNode
	var execItems []ToolNode
	var diffItems []ToolNode
	var otherItems []ToolNode

	for _, node := range m.toolTree {
		switch node.Name {
		case "grep", "glob", "read", "lsp":
			searchItems = append(searchItems, node)
		case "bash":
			execItems = append(execItems, node)
		case "edit", "write":
			diffItems = append(diffItems, node)
		default:
			otherItems = append(otherItems, node)
		}
	}

	if len(searchItems) > 0 {
		lines = append(lines, renderSearchBlock(searchItems, blockWidth)...)
		lines = append(lines, "")
	}
	if len(otherItems) > 0 {
		lines = append(lines, renderExecBlock(otherItems, blockWidth)...)
		lines = append(lines, "")
	}
	if len(execItems) > 0 {
		lines = append(lines, renderExecBlock(execItems, blockWidth)...)
		lines = append(lines, "")
	}
	// Only render diff block for completed edit/write tools that have content.
	// Pending tools (not yet Done) would render as an empty styled block.
	var doneDiffItems []ToolNode
	for _, node := range diffItems {
		if node.Done {
			doneDiffItems = append(doneDiffItems, node)
		}
	}
	if len(doneDiffItems) > 0 {
		lines = append(lines, m.renderDiffBlock(doneDiffItems, blockWidth)...)
		lines = append(lines, "")
	}

	return lines
}

func renderSearchBlock(nodes []ToolNode, width int) []string {
	var content []string
	header := SpinnerStyle.Render("▍") + " [?] " + SpinnerStyle.Render("Search")
	content = append(content, header)
	content = append(content, "")
	for _, node := range nodes {
		icon := node.Icon
		if icon == "" {
			icon = toolIcon(node.Name)
		}
		status := ""
		if node.Done {
			status = " " + SpinnerDoneStyle.Render("✓")
		}
		content = append(content, fmt.Sprintf("  %s  %s%s", icon, nodeDetailLabel(node), status))
		for _, child := range node.Children {
			detail := child.Detail
			if len(detail) > width-8 {
				detail = detail[:width-11] + "..."
			}
			content = append(content, DimStyle.Render("      "+detail))
		}
	}
	return strings.Split(SearchBlockStyle.Width(width).Render(strings.Join(content, "\n")), "\n")
}

// nodeDetailLabel returns the text shown after a tool node's icon. When the
// engine didn't supply a Detail (e.g. for activate_skill / handoff_to_agent /
// MCP tools whose arg shape isn't recognized), it falls back to the tool name
// so the node never renders as a bare icon with no context.
func nodeDetailLabel(node ToolNode) string {
	if strings.TrimSpace(node.Detail) != "" {
		return node.Detail
	}
	if node.Name != "" {
		return node.Name
	}
	return "—"
}

func renderExecBlock(nodes []ToolNode, width int) []string {
	var content []string
	header := SpinnerStyle.Render("▍") + " [>_] " + SpinnerStyle.Render("Execute")
	content = append(content, header)
	content = append(content, "")
	for _, node := range nodes {
		icon := node.Icon
		if icon == "" {
			icon = toolIcon(node.Name)
		}
		status := ""
		if node.Done {
			status = " " + SpinnerDoneStyle.Render("✓")
		}
		content = append(content, fmt.Sprintf("  %s  %s%s", icon, nodeDetailLabel(node), status))
		for _, child := range node.Children {
			detail := child.Detail
			if len(detail) > width-8 {
				detail = detail[:width-11] + "..."
			}
			content = append(content, DimStyle.Render("      "+detail))
		}
	}
	return strings.Split(ExecBlockStyle.Width(width).Render(strings.Join(content, "\n")), "\n")
}

func (m Model) renderDiffBlock(nodes []ToolNode, width int) []string {
	var content []string
	header := SpinnerStyle.Render("▍") + " [~] " + SpinnerStyle.Render("Changes")
	content = append(content, header)
	content = append(content, "")
	hunkSeq := 0 // global 1-based hunk number across all files
	for _, node := range nodes {
		status := ""
		if node.Done {
			status = " " + SpinnerDoneStyle.Render("✓")
		}
		content = append(content, fmt.Sprintf("  :: %s%s", node.Detail, status))
		if node.Done && len(node.Children) > 0 {
			for _, child := range node.Children {
				if child.DetailFull == "" {
					continue
				}
				hunkSeq++
				adds, deletes := countHunkAddsDeletes(child.DetailFull)
				content = append(content, hunkSummaryLine(hunkSeq-1, child.Detail, adds, deletes))
			}
		}
	}
	// R3: 保留首尾空行作为视觉间隔，不 pad 全宽（View 统一 ansi.Truncate）。
	result := make([]string, 0, len(content)+2)
	result = append(result, "")
	result = append(result, content...)
	result = append(result, "")
	return result
}

func renderToolSummary(toolTree []ToolNode) string {
	var b strings.Builder
	modified := 0
	for _, n := range toolTree {
		if n.Done && (n.Name == "edit" || n.Name == "write") && len(n.Children) > 0 {
			modified++
		}
	}
	b.WriteString(fmt.Sprintf("● %d tools executed, %d files modified\n", len(toolTree), modified))

	hunkSeq := 0 // global 1-based hunk number across all files
	for _, node := range toolTree {
		icon := node.Icon
		if icon == "" {
			icon = toolIcon(node.Name)
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", icon, node.Detail))
		for _, child := range node.Children {
			if node.Name == "edit" || node.Name == "write" {
				hunkContent := child.DetailFull
				if hunkContent != "" {
					hunkSeq++
					adds, deletes := countHunkAddsDeletes(hunkContent)
					b.WriteString(hunkSummaryLine(hunkSeq-1, child.Detail, adds, deletes) + "\n")
				}
			} else {
				b.WriteString(fmt.Sprintf("    %s\n", child.Detail))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
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
	// Collapse 3+ consecutive newlines to \n\n: raw LLM markdown often
	// contains excessive blank lines that glamour (used for final display)
	// normalizes, but wrapText renders literally — producing visual gaps.
	normalized := streaming
	for strings.Contains(normalized, "\n\n\n") {
		normalized = strings.ReplaceAll(normalized, "\n\n\n", "\n\n")
	}
	return wrapText(AssistantMsgStyle.Render(normalized), width)
}

func renderSpinners(spinners []AgentSpinner, width int) []string {
	if len(spinners) == 0 {
		return []string{}
	}
	lines := []string{}
	for _, spinner := range spinners {
		if spinner.Active {
			frame := spinnerFrames[spinner.FrameIdx%len(spinnerFrames)]
			line := fmt.Sprintf("  %s  %s: %s", frame, spinner.Role, spinner.Goal)
			lines = append(lines, SpinnerStyle.Render(line))
		} else {
			line := fmt.Sprintf("  ✓  %s: %s", spinner.Role, spinner.Summary)
			lines = append(lines, SpinnerDoneStyle.Render(line))
		}
	}
	return wrapLines(lines, width)
}

// renderSubAgentPanel renders the sub-agent parallel execution panel above the input.
// Shows each dispatched sub-agent with its type icon, goal, and status
// (spinner for running, checkmark for done). This is the primary visual
// indicator when multiple sub-agents are working in parallel.
func renderSubAgentPanel(agents []SubAgentStatus, width int) []string {
	if len(agents) == 0 {
		return nil
	}
	var content []string
	content = append(content, DimStyle.Render("▍")+" [→] "+DimStyle.Render("Sub-Agents"))
	content = append(content, "")
	for _, a := range agents {
		icon := agentIcon(a.Agent)
		goal := a.Goal
		if len(goal) > 60 {
			goal = goal[:60] + "..."
		}
		switch a.Status {
		case "running":
			frame := spinnerFrames[0]
			line := fmt.Sprintf("  %s %s %s  %s", frame, icon, a.Agent, SpinnerStyle.Render(goal))
			content = append(content, line)
		case "done":
			summary := a.Summary
			if len(summary) > 60 {
				summary = summary[:60] + "..."
			}
			line := fmt.Sprintf("  ✓ %s %s  %s", icon, a.Agent, SpinnerDoneStyle.Render(summary))
			content = append(content, line)
		case "error":
			line := fmt.Sprintf("  ✗ %s %s  ❌", icon, a.Agent)
			content = append(content, ErrorStyle.Render(line))
		}
	}
	rendered := ExecBlockStyle.Width(width).Render(strings.Join(content, "\n"))
	rawLines := strings.Split(rendered, "\n")
	var result []string
	for _, l := range rawLines {
		result = append(result, wrapLineAnsi(l, width)...)
	}
	return result
}

// agentIcon returns a default emoji for known agent types.
func agentIcon(agent string) string {
	switch agent {
	case "sub":
		return "🔍"
	case "critic":
		return "🔎"
	case "team-lead":
		return "👑"
	default:
		return "🤖"
	}
}

// renderMemberProgress renders roundtable member status cards above the input.
// Each member shows as a compact card: avatar + name + status (running spinner
// or done checkmark with score). This replaces the thinking box during review.
func renderMemberProgress(members []MemberStatus, width int) []string {
	if len(members) == 0 {
		return nil
	}
	var content []string
	content = append(content, DimStyle.Render("▍")+" [::] "+DimStyle.Render("Multi-Agent Review"))
	content = append(content, "")
	for _, m := range members {
		switch m.Status {
		case "running":
			frame := spinnerFrames[0]
			line := fmt.Sprintf("  %s %s %s  %s", frame, m.Avatar, m.Name, SpinnerStyle.Render("reviewing..."))
			content = append(content, line)
		case "done":
			verdictIcon := "✅"
			switch m.Verdict {
			case "conditional":
				verdictIcon = "⚠️"
			case "reject":
				verdictIcon = "❌"
			}
			line := fmt.Sprintf("  ✓ %s %s  %s  score: %d", m.Avatar, m.Name, verdictIcon, m.Score)
			content = append(content, SpinnerDoneStyle.Render(line))
		case "error":
			line := fmt.Sprintf("  ✗ %s %s  ❌ error", m.Avatar, m.Name)
			content = append(content, ErrorStyle.Render(line))
		}
	}
	rendered := ExecBlockStyle.Width(width).Render(strings.Join(content, "\n"))
	rawLines := strings.Split(rendered, "\n")
	var result []string
	for _, l := range rawLines {
		result = append(result, wrapLineAnsi(l, width)...)
	}
	return result
}

// tddPhaseMeta maps phase names to their display metadata.
var tddPhaseMeta = map[string]struct {
	Label   string
	Emoji   string
	PhaseID int // order: red=0, red_verify=1, green=2, green_verify=3, refactor=4
}{
	"red":          {"RED", "🔴", 0},
	"red_verify":   {"VERIFY", "🔍", 1},
	"green":        {"GREEN", "🟢", 2},
	"green_verify": {"VERIFY", "🔍", 3},
	"refactor":     {"REFACTOR", "♻️", 4},
}

// renderTDDStatus renders the TDD (Red-Green-Refactor) status block.
// Shows each stage with its completion status: waiting (⬜), running (emoji), done (✅).
func renderTDDStatus(stages []TDDStage, maxWidth int) []string {
	if len(stages) == 0 {
		return nil
	}
	var content []string
	content = append(content, DimStyle.Render("▍")+" [::] "+DimStyle.Render("TDD: test-driven-development"))
	content = append(content, "")

	// Build ordered list of stages (red, red_verify, green, green_verify, refactor)
	ordered := []struct {
		Phase  string
		Status string
		Detail string
	}{
		{Phase: "red", Status: "waiting", Detail: "编写失败测试..."},
		{Phase: "red_verify", Status: "waiting", Detail: "验证测试失败..."},
		{Phase: "green", Status: "waiting", Detail: "编写最小实现..."},
		{Phase: "green_verify", Status: "waiting", Detail: "验证测试通过..."},
		{Phase: "refactor", Status: "waiting", Detail: "清理代码..."},
	}

	// Override with actual stage data
	stageMap := make(map[string]TDDStage)
	for _, s := range stages {
		stageMap[s.Phase] = s
	}

	for i, o := range ordered {
		if actual, ok := stageMap[o.Phase]; ok {
			ordered[i].Status = actual.Status
			if actual.Detail != "" {
				ordered[i].Detail = actual.Detail
			}
		}
	}

	// Render each stage
	for _, o := range ordered {
		meta := tddPhaseMeta[o.Phase]
		phaseLabel := meta.Emoji + " " + meta.Label
		var line string
		switch o.Status {
		case "running":
			frame := spinnerFrames[0]
			line = fmt.Sprintf("  %s  %s   %s  %s",
				frame, phaseLabel, SpinnerStyle.Render(o.Detail), DimStyle.Render("running"))
		case "done":
			line = fmt.Sprintf("  ✓   %s   %s", phaseLabel, SpinnerDoneStyle.Render(o.Detail))
		default:
			line = fmt.Sprintf("  ·   %s   %s", phaseLabel, DimStyle.Render(o.Detail))
		}
		content = append(content, line)
	}

	rendered := ExecBlockStyle.Width(maxWidth).Render(strings.Join(content, "\n"))
	rawLines := strings.Split(rendered, "\n")
	var result []string
	for _, l := range rawLines {
		result = append(result, wrapLineAnsi(l, maxWidth)...)
	}
	return result
}

// renderOverlayStatus renders both TDD phases and member progress in a single
// overlay block. When both are present, they're displayed side-by-side (left/right)
// with a vertical divider.
func renderOverlayStatus(tddStages []TDDStage, members []MemberStatus, width int) []string {
	tddActive := len(tddStages) > 0
	memberActive := len(members) > 0

	if !tddActive && !memberActive {
		return nil
	}

	if tddActive && memberActive {
		// Side-by-side layout: split width in half
		halfWidth := (width - 3) / 2 // account for divider and spacing
		if halfWidth < 30 {
			halfWidth = 30
		}
		leftLines := renderTDDStatus(tddStages, halfWidth)
		rightLines := renderMemberProgress(members, halfWidth)

		// Combine side by side
		maxLines := len(leftLines)
		if len(rightLines) > maxLines {
			maxLines = len(rightLines)
		}

		// Pad both columns to same height
		for len(leftLines) < maxLines {
			leftLines = append(leftLines, "")
		}
		for len(rightLines) < maxLines {
			rightLines = append(rightLines, "")
		}

		divider := DimStyle.Render(" ┃ ")
		combined := make([]string, maxLines)
		for i := 0; i < maxLines; i++ {
			l := leftLines[i]
			r := rightLines[i]
			// Truncate or pad left column
			lStr := stripAnsi(l)
			if len(lStr) > halfWidth {
				l = truncateAnsi(l, halfWidth)
			} else if len(lStr) < halfWidth {
				l += strings.Repeat(" ", halfWidth-len(lStr))
			}
			combined[i] = l + divider + r
		}
		return combined
	}

	if tddActive {
		// Ensure TDD panel gets full width (renderTDDStatus handles this via maxWidth)
		return renderTDDStatus(tddStages, width)
	}

	return renderMemberProgress(members, width)
}

// truncateAnsi truncates a string containing ANSI escape codes to the given
// visible width, preserving escape sequences.
func truncateAnsi(s string, maxWidth int) string {
	visible := 0
	var result strings.Builder
	var buf strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			buf.Reset()
			buf.WriteRune(r)
			continue
		}
		if inEscape {
			buf.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
				result.WriteString(buf.String())
				buf.Reset()
			}
			continue
		}
		if visible >= maxWidth {
			continue
		}
		result.WriteRune(r)
		visible++
	}
	return result.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

const thinkingBoxHeight = 3

func renderThinkingBox(activity string, width int) []string {
	if activity == "" {
		return nil
	}
	blockWidth := width - 6
	if blockWidth < 20 {
		blockWidth = 20
	}

	// Trim agent name prefix if present in format "agentName: activity"
	display := activity
	if idx := strings.Index(activity, ": "); idx > 0 {
		name := activity[:idx]
		task := activity[idx+2:]
		icon := "⚙️"
		switch name {
		case "deepact":
			icon = ""
		case "sub":
			icon = "🔍"
		case "critic":
			icon = "🔎"
		}
		display = icon + " " + name + ": " + task
	} else {
		display = "⚙️ " + activity
	}

	// Truncate if too wide
	if len(display) > blockWidth {
		display = ansi.Truncate(display, blockWidth, "…")
	}

	var lines []string
	lines = append(lines, "  "+DimStyle.Render(display))
	for len(lines) < thinkingBoxHeight {
		lines = append(lines, "")
	}
	rendered := ThinkingBlockStyle.Width(width).Render(strings.Join(lines, "\n"))
	return strings.Split(rendered, "\n")
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
	return SuggestionBox.Width(width - 2).Render(content)
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
	switch runtime.GOOS {
	case "windows":
		b.WriteString("| `Ctrl+Enter` | Insert newline |\n")
	case "darwin":
		b.WriteString("| `⌥+Enter` | Insert newline |\n")
	default:
		b.WriteString("| `Alt+Enter` | Insert newline |\n")
	}
	switch runtime.GOOS {
	case "darwin":
		b.WriteString("| `⌥+drag` | Select text (bypasses mouse scroll) |\n")
	default:
		b.WriteString("| `Shift+drag` | Select text (bypasses mouse scroll) |\n")
	}
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
	return SuggestionBox.Width(width - 2).Render(content)
}

func renderInputLine(m Model) string {
	if m.state == stateApiKeyPrompt {
		content := "  Key> " + strings.Repeat("*", len(m.apiKeyInput)) + "█"
		padW := m.width - lipgloss.Width(content)
		if padW > 0 {
			content += strings.Repeat(" ", padW)
		}
		return StatusBarStyle.Render(content)
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

	innerWidth := m.width - 6
	if innerWidth < 20 {
		innerWidth = 20
	}

	text := left + cursor + right
	wrapped := wrapInputText(text, innerWidth)
	wLines := strings.Split(wrapped, "\n")

	var rows []string

	// Blue bar on the left — rendered separately to avoid ANSI nesting
	// (InputBarStyle.Render emits \033[0m which would kill InputBlockStyle's background)
	bar := "▍"

	// Separator row: blue bar + blank area
	rows = append(rows,
		InputBarStyle.Render(bar)+
			InputBlockStyle.Render(strings.Repeat(" ", m.width-1)))

	for i, line := range wLines {
		var prefix string
		if i == 0 {
			prefix = "  " + "> "
		} else {
			prefix = "    "
		}
		content := prefix + line
		padW := m.width - 1 - lipgloss.Width(content)
		if padW > 0 {
			content += strings.Repeat(" ", padW)
		}
		rows = append(rows,
			InputBarStyle.Render(bar)+
				InputBlockStyle.Render(content))
	}

	return strings.Join(rows, "\n")
}

func wrapInputText(text string, width int) string {
	lines := strings.Split(text, "\n")
	var result []string
	for _, line := range lines {
		if lipgloss.Width(line) <= width {
			result = append(result, line)
			continue
		}
		// Hard-wrap by visual width, not rune count.
		// This correctly handles CJK characters (visual width 2) and emoji.
		runes := []rune(line)
		var chunk strings.Builder
		chunkWidth := 0
		for _, r := range runes {
			rw := lipgloss.Width(string(r))
			if chunkWidth+rw > width && chunkWidth > 0 {
				result = append(result, chunk.String())
				chunk.Reset()
				chunkWidth = 0
			}
			chunk.WriteRune(r)
			chunkWidth += rw
		}
		if chunkWidth > 0 {
			result = append(result, chunk.String())
		}
	}
	return strings.Join(result, "\n")
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

func renderStatusBar(status StatusInfo, scrollOffset, scrollMax int, width int, clipboardFeedback time.Time, clipboardError string) string {
	dragHint := "Drag to select"
	if !clipboardFeedback.IsZero() && time.Since(clipboardFeedback) < 2*time.Second {
		if clipboardError != "" {
			dragHint = "⚠ Copy failed"
		} else {
			dragHint = "✓ Copied"
		}
	}
	newlineHint := "Alt+↩"
	switch runtime.GOOS {
	case "windows":
		newlineHint = "Ctrl+↩"
	case "darwin":
		newlineHint = "⌥+↩"
	}

	// Compute cache hit rate as integer percentage
	var cacheRate int
	if status.TokensIn > 0 {
		cacheRate = int(float64(status.CacheHitTokens) / float64(status.TokensIn) * 100)
		if cacheRate > 100 {
			cacheRate = 100
		}
	}
	cacheStr := fmt.Sprintf("%d%%", cacheRate)

	leftPart := fmt.Sprintf(" %s ↑%.1fK ↓%.1fK", cacheStr, float64(status.TokensIn)/1000.0, float64(status.TokensOut)/1000.0)
	rightPart := fmt.Sprintf("%s │ %s │ Esc │ ^Q", dragHint, newlineHint)

	// Reserve 1 column for the blue bar on the left
	contentWidth := width - 1
	if contentWidth < 1 {
		contentWidth = 1
	}

	leftW := lipgloss.Width(leftPart)
	rightW := lipgloss.Width(rightPart)
	gap := contentWidth - leftW - rightW
	if gap < 1 {
		gap = 1
	}
	line := leftPart + strings.Repeat(" ", gap) + rightPart
	// Use ansi.Truncate to guarantee the line fits within contentWidth.
	// Characters like ↑ ↓ ⌥ ↩ │ have ambiguous East Asian Width on macOS —
	// lipgloss.Width may underestimate their rendered width, causing terminal
	// line wrapping that pushes the input area off-screen.
	line = ansi.Truncate(line, contentWidth, "")
	// Defense-in-depth: ensure rendered width exactly fills contentWidth.
	// Ambiguous-width characters (↑↓│⌥↩) may cause the terminal to render
	// narrower than ansi.Truncate expects, leaving old characters visible.
	if w := lipgloss.Width(line); w < contentWidth {
		line += strings.Repeat(" ", contentWidth-w)
	}
	// Render ALL THREE rows as a SINGLE lipgloss block: bg set once, fg
	// inlined via ANSI codes, single \033[0m at the end. This avoids the
	// intermediate resets between bar+content per-line and between lines,
	// both of which cause background loss on Windows terminals.
	// Colors respect dark/light mode via StatusBarStyle values (方案A).
	var bgColor, fgBarColor, fgContentColor string
	if isDark {
		bgColor = "236"
		fgBarColor = "68"
		fgContentColor = "250"
	} else {
		bgColor = "255"
		fgBarColor = "25"
		fgContentColor = "236"
	}
	bgStyle := lipgloss.NewStyle().Background(lipgloss.Color(bgColor))
	fgBar := fmt.Sprintf("\033[38;5;%sm", fgBarColor)
	fgContent := fmt.Sprintf("\033[38;5;%sm", fgContentColor)
	rows := strings.Join([]string{
		fgBar + "▍" + fgContent + strings.Repeat(" ", contentWidth),
		fgBar + "▍" + fgContent + line,
		fgBar + "▍" + fgContent + strings.Repeat(" ", contentWidth),
	}, "\n")
	return bgStyle.Render(rows)
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
	if lipgloss.Width(line) <= width {
		return []string{line}
	}
	// Delegate ANSI-containing lines to wrapLineAnsi for safe wrapping
	if strings.Contains(line, "\x1b[") {
		return wrapLineAnsi(line, width)
	}
	runes := []rune(line)
	var lines []string
	for len(runes) > 0 {
		// Measure visual width of remaining text
		if lipgloss.Width(string(runes)) <= width {
			lines = append(lines, string(runes))
			break
		}
		// Scan forward by visual width to find break point
		var visWidth int
		var lastSpaceIdx = -1
		var cutIdx = len(runes)
		for i, r := range runes {
			rw := lipgloss.Width(string(r))
			if visWidth+rw > width {
				cutIdx = i
				break
			}
			if r == ' ' || r == '　' {
				lastSpaceIdx = i
			}
			visWidth += rw
		}
		// Word-wrap: prefer breaking at last space within width
		if lastSpaceIdx >= 0 {
			cutIdx = lastSpaceIdx
		}
		// Ensure at least one character is taken
		if cutIdx == 0 {
			cutIdx = 1
		}
		lines = append(lines, string(runes[:cutIdx]))
		runes = runes[cutIdx:]
		if len(runes) > 0 && (runes[0] == ' ' || runes[0] == '　') {
			runes = runes[1:]
		}
	}
	return lines
}

// isSGR checks whether an ANSI escape sequence is an SGR (Select Graphic
// Rendition) sequence, i.e. it sets text style attributes like colors.
// SGR sequences match the pattern \x1b[...m where ... is one or more
// semicolon-separated numbers, or empty for reset.
func isSGR(seq string) bool {
	if len(seq) < 3 || seq[len(seq)-1] != 'm' {
		return false
	}
	params := seq[2 : len(seq)-1]
	if params == "" {
		return true // \x1b[m is SGR reset
	}
	for _, c := range params {
		if (c < '0' || c > '9') && c != ';' {
			return false
		}
	}
	return true
}

// wrapLineAnsi wraps a line that may contain ANSI escape sequences to fit
// within the given visual width. It preserves all escape sequences intact,
// re-emits active SGR sequences at the start of continuation lines, and
// emits SGR reset at the end of each wrapped line to prevent color bleeding.
//
// Word-wrap: prefers breaking at spaces (U+0020, U+3000). Falls back to
// hard-break at width boundary when no space is found within the segment.
func wrapLineAnsi(line string, width int) []string {
	if width <= 0 || line == "" {
		return []string{line}
	}
	if lipgloss.Width(line) <= width {
		return []string{line}
	}

	var lines []string
	var curLine strings.Builder
	var activeSGRs []string
	visualCol := 0
	lastSpaceIdx := -1

	flushLine := func() {
		s := curLine.String()
		if len(activeSGRs) > 0 {
			s += "\x1b[0m"
		}
		lines = append(lines, s)
		curLine.Reset()
		for _, sgr := range activeSGRs {
			curLine.WriteString(sgr)
		}
		visualCol = 0
		lastSpaceIdx = -1
	}

	runes := []rune(line)
	i := 0
	for i < len(runes) {
		r := runes[i]

		if r == '\x1b' {
			escBuf := strings.Builder{}
			escBuf.WriteRune(r)
			i++
			for i < len(runes) {
				r2 := runes[i]
				escBuf.WriteRune(r2)
				i++
				if (r2 >= 'a' && r2 <= 'z') || (r2 >= 'A' && r2 <= 'Z') {
					break
				}
			}
			seq := escBuf.String()
			curLine.WriteString(seq)

			if isSGR(seq) {
				if seq == "\x1b[0m" || seq == "\x1b[m" {
					activeSGRs = nil
				} else {
					activeSGRs = append(activeSGRs, seq)
				}
			}
			continue
		}

		rw := lipgloss.Width(string(r))

		if visualCol+rw > width {
			if lastSpaceIdx >= 0 {
				curLineStr := curLine.String()
				trimmed := curLineStr[:lastSpaceIdx]
				overflow := curLineStr[lastSpaceIdx:]
				if len(overflow) > 0 {
					if overflow[0] == ' ' {
						overflow = overflow[1:]
					} else if strings.HasPrefix(overflow, "　") {
						overflow = overflow[len("　"):]
					}
				}
				curLine.Reset()
				curLine.WriteString(trimmed)
				flushLine()
				curLine.WriteString(overflow)
				curLine.WriteRune(r)
				visualCol = lipgloss.Width(stripAnsi(overflow)) + rw
				lastSpaceIdx = -1
				i++
				continue
			}
			flushLine()
			curLine.WriteRune(r)
			visualCol = rw
			if r == ' ' || r == '　' {
				lastSpaceIdx = len([]byte(curLine.String())) - len(string(r))
			}
			i++
			continue
		}

		curLine.WriteRune(r)
		visualCol += rw
		if r == ' ' || r == '　' {
			lastSpaceIdx = len([]byte(curLine.String())) - len(string(r))
		}
		i++
	}

	if curLine.Len() > 0 {
		if len(lines) == 0 {
			lines = append(lines, line)
		} else {
			lines = append(lines, curLine.String())
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
		if lipgloss.Width(line) <= width {
			result = append(result, line)
		} else {
			result = append(result, wrapLine(line, width)...)
		}
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
