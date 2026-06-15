package ui

import "testing"

func TestNormalizeSelection_StartBeforeEnd(t *testing.T) {
	s := SelectionState{
		Active: true,
		Start:  selPoint{Line: 2, Col: 5},
		End:    selPoint{Line: 8, Col: 10},
	}
	start, end := normalizeSelection(s)
	if start.Line != 2 || start.Col != 5 {
		t.Errorf("start: want {2,5}, got {%d,%d}", start.Line, start.Col)
	}
	if end.Line != 8 || end.Col != 10 {
		t.Errorf("end: want {8,10}, got {%d,%d}", end.Line, end.Col)
	}
}

func TestNormalizeSelection_StartAfterEnd(t *testing.T) {
	s := SelectionState{
		Active: true,
		Start:  selPoint{Line: 8, Col: 10},
		End:    selPoint{Line: 2, Col: 5},
	}
	start, end := normalizeSelection(s)
	if start.Line != 2 || start.Col != 5 {
		t.Errorf("start: want {2,5}, got {%d,%d}", start.Line, start.Col)
	}
	if end.Line != 8 || end.Col != 10 {
		t.Errorf("end: want {8,10}, got {%d,%d}", end.Line, end.Col)
	}
}

func TestNormalizeSelection_SameLineColReversed(t *testing.T) {
	s := SelectionState{
		Active: true,
		Start:  selPoint{Line: 5, Col: 20},
		End:    selPoint{Line: 5, Col: 3},
	}
	start, end := normalizeSelection(s)
	if start.Line != 5 || start.Col != 3 {
		t.Errorf("start: want {5,3}, got {%d,%d}", start.Line, start.Col)
	}
	if end.Line != 5 || end.Col != 20 {
		t.Errorf("end: want {5,20}, got {%d,%d}", end.Line, end.Col)
	}
}

func TestNormalizeSelection_EmptySelection(t *testing.T) {
	s := SelectionState{}
	start, end := normalizeSelection(s)
	if start != (selPoint{}) || end != (selPoint{}) {
		t.Errorf("empty selection should return zero values")
	}
}

func TestScreenToLine_Basic(t *testing.T) {
	m := Model{height: 40}
	m.cachedTotalLines = 100
	m.msgCache = &messageRenderCache{lastMaxScroll: 60}

	// scrollOffset=0, bodyHeight = 40 - footerHeight(~4) = 36
	// visible: [100-0-36, 100-0-1] = [64, 99]
	// screenRow 0 → content line 64
	pt := m.screenToLine(0, 5)
	if pt.Line != 64 {
		t.Errorf("screenToLine(0,5): want line 64, got %d", pt.Line)
	}
	if pt.Col != 5 {
		t.Errorf("screenToLine(0,5): want col 5, got %d", pt.Col)
	}
}

func TestScreenToLine_ClampBeyondContent(t *testing.T) {
	m := Model{height: 40}
	m.cachedTotalLines = 10
	m.msgCache = &messageRenderCache{lastMaxScroll: 0}

	// bodyHeight = 40 - 4 = 36, totalLines = 10 < bodyHeight
	// startLine = 10 - 0 - 36 = -26 (content at bottom of visible area)
	// screenRow 9 → lineIdx = -26 + 9 = -17, clamped to 0 (first content line)
	// screenRow 35 → lineIdx = -26 + 35 = 9 (last content line)
	pt := m.screenToLine(35, 0)
	if pt.Line != 9 {
		t.Errorf("clamp to last line: want line 9, got %d", pt.Line)
	}
	// Negative lineIdx should clamp to 0
	pt2 := m.screenToLine(0, 0)
	if pt2.Line < 0 {
		t.Errorf("should clamp to 0, got %d", pt2.Line)
	}
}

func TestApplySelectionHighlight_NoSelection(t *testing.T) {
	m := Model{}
	lines := []string{"hello", "world"}
	result := m.applySelectionHighlight(lines)
	if len(result) != 2 || result[0] != "hello" || result[1] != "world" {
		t.Error("no selection: lines should be unchanged")
	}
}

func TestApplySelectionHighlight_WithSelection(t *testing.T) {
	m := Model{
		selection: SelectionState{
			Done:  true,
			Start: selPoint{Line: 0, Col: 0},
			End:   selPoint{Line: 1, Col: -1},
		},
	}
	lines := []string{"hello", "world", "extra"}
	result := m.applySelectionHighlight(lines)
	// Lines 0 and 1 should have reverse markers
	if !containsSeq(result[0], "\x1b[7m") {
		t.Errorf("line 0 should have reverse marker, got %q", result[0])
	}
	if !containsSeq(result[1], "\x1b[7m") {
		t.Errorf("line 1 should have reverse marker, got %q", result[1])
	}
	// Line 2 should be unchanged
	if result[2] != "extra" {
		t.Errorf("line 2 should be unchanged, got %q", result[2])
	}
}

func containsSeq(s, seq string) bool {
	for i := 0; i <= len(s)-len(seq); i++ {
		if s[i:i+len(seq)] == seq {
			return true
		}
	}
	return false
}
