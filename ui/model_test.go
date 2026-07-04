package ui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/deepact/deepact/engine"
)

// TestScrollWithPgKeys verifies that keyboard scrolling via PgUp/PgDown
// works correctly.
func TestScrollWithPgKeys(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 40

	upMsg := tea.KeyMsg{Type: tea.KeyPgUp}
	result, _ := m.Update(upMsg)
	m2 := result.(Model)
	if m2.scrollOffset != 20 {
		t.Errorf("PgUp: want scrollOffset=20, got %d", m2.scrollOffset)
	}

	downMsg := tea.KeyMsg{Type: tea.KeyPgDown}
	result, _ = m2.Update(downMsg)
	m3 := result.(Model)
	if m3.scrollOffset != 0 {
		t.Errorf("PgDown: want scrollOffset=0, got %d", m3.scrollOffset)
	}

	result, _ = m3.Update(downMsg)
	m4 := result.(Model)
	if m4.scrollOffset != 0 {
		t.Errorf("PgDown at 0: want scrollOffset=0, got %d", m4.scrollOffset)
	}

	m4.scrollOffset = 0
	result, _ = m4.Update(upMsg)
	m5 := result.(Model)
	result, _ = m5.Update(upMsg)
	m6 := result.(Model)
	if m6.scrollOffset != 40 {
		t.Errorf("PgUp x2: want scrollOffset=40, got %d", m6.scrollOffset)
	}
	result, _ = m6.Update(downMsg)
	m7 := result.(Model)
	if m7.scrollOffset != 20 {
		t.Errorf("PgUp x2 + PgDown: want scrollOffset=20, got %d", m7.scrollOffset)
	}
}

// TestNoMouseTracking verifies the model doesn't crash when
// WithMouseCellMotion is not enabled.
func TestNoMouseTracking(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady

	tickMsg := TickMsg{}
	result, _ := m.Update(tickMsg)
	if result == nil {
		t.Fatal("model returned nil after TickMsg")
	}

	winMsg := tea.WindowSizeMsg{Width: 100, Height: 40}
	result, _ = m.Update(winMsg)
	if result == nil {
		t.Fatal("model returned nil after WindowSizeMsg")
	}
}

// TestMouseWheelScroll verifies that MouseWheelUp/Down events scroll the view.
func TestMouseWheelScroll(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 40
	m.scrollOffset = 0

	downMsg := tea.MouseMsg{Button: tea.MouseButtonWheelDown}
	result, _ := m.Update(downMsg)
	m2 := result.(Model)
	if m2.scrollOffset != 0 {
		t.Errorf("WheelDown at 0: want scrollOffset=0, got %d", m2.scrollOffset)
	}

	upMsg := tea.MouseMsg{Button: tea.MouseButtonWheelUp}
	result, _ = m2.Update(upMsg)
	m3 := result.(Model)
	if m3.scrollOffset != 13 {
		t.Errorf("WheelUp: want scrollOffset=13, got %d", m3.scrollOffset)
	}

	result, _ = m3.Update(downMsg)
	m4 := result.(Model)
	if m4.scrollOffset != 0 {
		t.Errorf("WheelDown: want scrollOffset=0, got %d", m4.scrollOffset)
	}

	m4.state = stateRunning
	result, _ = m4.Update(upMsg)
	m5 := result.(Model)
	if m5.scrollOffset != 13 {
		t.Errorf("WheelUp during running: want scrollOffset=13, got %d", m5.scrollOffset)
	}
}

// TestRunningStatePgScroll verifies PgUp/PgDown works during running state.
func TestRunningStatePgScroll(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateRunning
	m.height = 40

	upMsg := tea.KeyMsg{Type: tea.KeyPgUp}
	result, _ := m.Update(upMsg)
	m2 := result.(Model)
	if m2.scrollOffset != 20 {
		t.Errorf("PgUp during running: want scrollOffset=20, got %d", m2.scrollOffset)
	}

	enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
	result, _ = m2.Update(enterMsg)
	if _, ok := result.(Model); !ok {
		t.Fatal("model returned non-Model after blocked key")
	}
}

// TestMouseDragSelection_StartDrag verifies mouse left-down starts selection.
func TestMouseDragSelection_StartDrag(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 40
	m.width = 100
	m.msgCache = &messageRenderCache{}
	m.messages = []DisplayMessage{{Role: "user", Content: "test message for selection"}}

	downMsg := tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
		Y:      0,
		X:      5,
	}
	result, _ := m.Update(downMsg)
	m2 := result.(Model)
	if !m2.selection.Active {
		t.Error("selection should be active after mouse down")
	}
	if m2.selection.Done {
		t.Error("selection should not be done during drag")
	}
}

// TestSelectionClearedOnKeyPress verifies key press clears selection.
func TestSelectionClearedOnKeyPress(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.selection = SelectionState{
		Done:  true,
		Start: selPoint{Line: 2, Col: 0},
		End:   selPoint{Line: 5, Col: 10},
	}

	keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	result, _ := m.Update(keyMsg)
	m2 := result.(Model)
	if m2.selection.Done || m2.selection.Active {
		t.Error("key press should clear selection")
	}
}

func TestStatusBarShowsCacheHitRate(t *testing.T) {
	tests := []struct {
		name          string
		tokensIn      int
		cacheHit      int
		wantSubstring string // expected in rendered status bar
	}{
		{
			name:          "75 percent hit rate",
			tokensIn:      10000,
			cacheHit:      7500,
			wantSubstring: "75%",
		},
		{
			name:          "zero hit rate",
			tokensIn:      10000,
			cacheHit:      0,
			wantSubstring: "0%",
		},
		{
			name:          "hundred percent hit rate",
			tokensIn:      5000,
			cacheHit:      5000,
			wantSubstring: "100%",
		},
		{
			name:          "no tokens yet shows zero",
			tokensIn:      0,
			cacheHit:      0,
			wantSubstring: "0%",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := StatusInfo{
				TokensIn:       tt.tokensIn,
				CacheHitTokens: tt.cacheHit,
			}
			line := renderStatusBar(status, 0, 0, 80, time.Time{}, "")
			if !strings.Contains(line, tt.wantSubstring) {
				t.Errorf("renderStatusBar wants %q in output, got: %q", tt.wantSubstring, line)
			}
		})
	}
}

func TestFooterHeightMatchesViewFooterHeight(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.width = 80
	m.height = 24

	inputLine := renderInputLine(m)
	viewFooterHeight := 3 + renderedHeight(inputLine)
	if got := m.footerHeight(); got != viewFooterHeight {
		t.Fatalf("footerHeight should match View footer height: got %d, want %d", got, viewFooterHeight)
	}
}

func TestAutoScrollTickTopEdgeScrollsUp(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 20
	m.width = 80
	m.autoScrollDir = -1
	m.lastMouseX = 5
	m.lastMouseY = 0
	// Auto-scroll now operates on the mouse-down snapshot (sel.Scroll), not the
	// live scrollOffset, so the selection stays anchored while dragging.
	m.selection = SelectionState{
		Active:     true,
		Start:      selPoint{Line: 99, Col: 5},
		End:        selPoint{Line: 99, Col: 5},
		Plain:      make([]string, 200),
		BodyHeight: 20,
		Scroll:     0,
	}

	result, _ := m.Update(autoScrollTickMsg{})
	m2 := result.(Model)
	if m2.selection.Scroll != 1 {
		t.Fatalf("top-edge auto-scroll should increase snapshot Scroll to scroll up, got %d", m2.selection.Scroll)
	}
}

func TestAutoScrollTickBottomEdgeScrollsDown(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 20
	m.width = 80
	m.autoScrollDir = 1
	m.lastMouseX = 5
	m.lastMouseY = 19
	m.selection = SelectionState{
		Active:     true,
		Start:      selPoint{Line: 50, Col: 5},
		End:        selPoint{Line: 50, Col: 5},
		Plain:      make([]string, 200),
		BodyHeight: 20,
		Scroll:     10,
	}

	result, _ := m.Update(autoScrollTickMsg{})
	m2 := result.(Model)
	if m2.selection.Scroll != 9 {
		t.Fatalf("bottom-edge auto-scroll should decrease snapshot Scroll to scroll down, got %d", m2.selection.Scroll)
	}
}

// TestModelMouseClickNoDrag verifies single click clears selection.
func TestModelMouseClickNoDrag(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 40
	m.width = 100
	m.msgCache = &messageRenderCache{}
	m.messages = []DisplayMessage{{Role: "user", Content: "test message"}}

	downMsg := tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
		Y:      0, X: 5,
	}
	result, _ := m.Update(downMsg)
	m2 := result.(Model)
	if !m2.selection.Active {
		t.Error("selection should be active after mouse down")
	}

	upMsg := tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease,
		Y:      0, X: 5,
	}
	result, _ = m2.Update(upMsg)
	m3 := result.(Model)
	if m3.selection.Done || m3.selection.Active {
		t.Error("single click should clear selection")
	}
}

// ---- wrapLineAnsi tests ----

func TestWrapLineAnsi_PlainASCII(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		width int
		want  []string
	}{
		{name: "short line no wrap", line: "hello", width: 10, want: []string{"hello"}},
		{name: "exact width no wrap", line: "hello world", width: 11, want: []string{"hello world"}},
		{name: "wrap at space", line: "hello world foo bar", width: 12, want: []string{"hello world", "foo bar"}},
		{name: "hard break when no space", line: "abcdefghijklmnop", width: 5, want: []string{"abcde", "fghij", "klmno", "p"}},
		{name: "empty line", line: "", width: 10, want: []string{""}},
		{name: "width zero", line: "hello world", width: 0, want: []string{"hello world"}},
		{name: "width negative", line: "hello world", width: -1, want: []string{"hello world"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapLineAnsi(tt.line, tt.width)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("wrapLineAnsi(%q, %d) = %q, want %q", tt.line, tt.width, got, tt.want)
			}
		})
	}
}

func TestWrapLineAnsi_WithSGR(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		width int
		want  []string
	}{
		{
			name:  "SGR wrapped at space",
			line:  "\x1b[31mhello world foo bar\x1b[0m",
			width: 12,
			want:  []string{"\x1b[31mhello world\x1b[0m", "\x1b[31mfoo bar\x1b[0m"},
		},
		{
			name:  "SGR hard break no space",
			line:  "\x1b[31mabcdefghij\x1b[0m",
			width: 5,
			want:  []string{"\x1b[31mabcde\x1b[0m", "\x1b[31mfghij\x1b[0m"},
		},
		{
			name:  "short line with SGR no wrap",
			line:  "\x1b[32mhello\x1b[0m",
			width: 10,
			want:  []string{"\x1b[32mhello\x1b[0m"},
		},
		{
			name:  "multiple SGR sequences",
			line:  "\x1b[1m\x1b[31mbold red text long enough to wrap\x1b[0m",
			width: 16,
			want:  []string{"\x1b[1m\x1b[31mbold red text\x1b[0m", "\x1b[1m\x1b[31mlong enough to\x1b[0m", "\x1b[1m\x1b[31mwrap\x1b[0m"},
		},
		{
			name:  "SGR in middle of text",
			line:  "normal \x1b[31mred text here\x1b[0m normal",
			width: 14,
			want:  []string{"normal \x1b[31mred\x1b[0m", "\x1b[31mtext here\x1b[0m", "normal"},
		},
		{
			name:  "non-SGR ANSI not replayed",
			line:  "\x1b[2Jhello world long text",
			width: 12,
			want:  []string{"\x1b[2Jhello world", "long text"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapLineAnsi(tt.line, tt.width)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("wrapLineAnsi(%q, %d) =\n  got:  %q\n  want: %q", tt.line, tt.width, got, tt.want)
			}
			for i, l := range got {
				if w := lipgloss.Width(l); w > tt.width && tt.width > 0 {
					t.Errorf("line %d visual width %d exceeds limit %d: %q", i, w, tt.width, l)
				}
			}
		})
	}
}

func TestWrapLineAnsi_WideChars(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		width int
		want  []string
	}{
		{name: "CJK wrap", line: "你好世界你好世界", width: 8, want: []string{"你好世界", "你好世界"}},
		{name: "CJK mixed with ASCII", line: "hello你好world世界", width: 10, want: []string{"hello你好w", "orld世界"}},
		{
			name:  "CJK with SGR",
			line:  "\x1b[32m你好世界你好世界\x1b[0m",
			width: 8,
			want:  []string{"\x1b[32m你好世界\x1b[0m", "\x1b[32m你好世界\x1b[0m"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapLineAnsi(tt.line, tt.width)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("wrapLineAnsi(%q, %d) =\n  got:  %q\n  want: %q", tt.line, tt.width, got, tt.want)
			}
		})
	}
}
