package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deepact/deepact/engine"
)

// TestScrollWithPgKeys verifies that keyboard scrolling via PgUp/PgDown
// works correctly. This is the replacement for mouse-wheel scrolling after
// removing WithMouseCellMotion (which blocked native terminal text selection).
func TestScrollWithPgKeys(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 40 // simulate a 40-row terminal

	// PgUp should increase scrollOffset by height/2 = 20
	upMsg := tea.KeyMsg{Type: tea.KeyPgUp}
	result, _ := m.Update(upMsg)
	m2 := result.(Model)
	if m2.scrollOffset != 20 {
		t.Errorf("PgUp: want scrollOffset=20, got %d", m2.scrollOffset)
	}

	// PgDown should decrease scrollOffset, clamped at 0
	downMsg := tea.KeyMsg{Type: tea.KeyPgDown}
	result, _ = m2.Update(downMsg)
	m3 := result.(Model)
	if m3.scrollOffset != 0 {
		t.Errorf("PgDown: want scrollOffset=0, got %d", m3.scrollOffset)
	}

	// PgDown at 0 should stay 0 (clamped)
	result, _ = m3.Update(downMsg)
	m4 := result.(Model)
	if m4.scrollOffset != 0 {
		t.Errorf("PgDown at 0: want scrollOffset=0, got %d", m4.scrollOffset)
	}

	// PgUp twice = 40, then PgDown once = 20
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

// TestNoMouseTracking verifies the model doesn't crash or produce unexpected
// behavior when WithMouseCellMotion is not enabled (native terminal selection).
// This is a smoke test — mouse events won't arrive via tea.MouseMsg, but the
// model should handle other messages gracefully without relying on mouse state.
func TestNoMouseTracking(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady

	// TickMsg should be handled without any mouse-related crash
	tickMsg := TickMsg{}
	result, _ := m.Update(tickMsg)
	if result == nil {
		t.Fatal("model returned nil after TickMsg")
	}

	// Window resize should be handled
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

	// Wheel down should decrease scrollOffset (clamped at 0)
	downMsg := tea.MouseMsg{Button: tea.MouseButtonWheelDown}
	result, _ := m.Update(downMsg)
	m2 := result.(Model)
	if m2.scrollOffset != 0 {
		t.Errorf("WheelDown at 0: want scrollOffset=0, got %d", m2.scrollOffset)
	}

	// Wheel up should increase scrollOffset
	upMsg := tea.MouseMsg{Button: tea.MouseButtonWheelUp}
	result, _ = m2.Update(upMsg)
	m3 := result.(Model)
	if m3.scrollOffset != 13 { // height/3 = 40/3 ≈ 13
		t.Errorf("WheelUp: want scrollOffset=13, got %d", m3.scrollOffset)
	}

	// Wheel down should decrease it
	result, _ = m3.Update(downMsg)
	m4 := result.(Model)
	if m4.scrollOffset != 0 {
		t.Errorf("WheelDown: want scrollOffset=0, got %d", m4.scrollOffset)
	}

	// Running state should also allow scroll
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

	// Normal input should still be blocked during running
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
	m.cachedTotalLines = 100
	m.msgCache = &messageRenderCache{lastMaxScroll: 60}

	downMsg := tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
		Y:      10,
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

// TestMouseClickNoDragClearsSelection verifies single click clears existing selection.
func TestMouseClickNoDragClearsSelection(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 40
	m.cachedTotalLines = 100
	m.msgCache = &messageRenderCache{lastMaxScroll: 60}

	// Single click (down + up at same position)
	downMsg := tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
		Y:      10, X: 5,
	}
	result, _ := m.Update(downMsg)
	m2 := result.(Model)
	if !m2.selection.Active {
		t.Error("selection should be active after mouse down")
	}

	upMsg := tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease,
		Y:      10, X: 5,
	}
	result, _ = m2.Update(upMsg)
	m3 := result.(Model)
	if m3.selection.Done || m3.selection.Active {
		t.Error("single click should clear selection")
	}
}
