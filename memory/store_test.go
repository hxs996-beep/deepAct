package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deepact/deepact/engine"
)

func TestNew(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if s == nil || s.Dir() != dir {
		t.Fatalf("expected store rooted at %s, got %v", dir, s)
	}
}

func TestNew_EmptyDir(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestLoad_MissingReturnsNil(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Load()
	if err != nil {
		t.Fatalf("Load() on empty store: %v", err)
	}
	if snap != nil {
		t.Fatalf("expected nil snapshot, got %+v", snap)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	now := time.Now()
	want := &engine.MemorySnapshot{
		CWD:           "/proj",
		UpdatedAt:     now,
		MemoryMarkers: []string{"bug root cause: X", "arch decision: Y"},
		Decisions:     []engine.Decision{{ID: "d-1", Text: "use interface injection"}},
		OpenQuestions: []string{"should we cache?"},
		Assumptions:   []string{"DeepSeek 1M ctx"},
	}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil snapshot after Save")
	}
	if got.CWD != want.CWD {
		t.Errorf("CWD = %q, want %q", got.CWD, want.CWD)
	}
	if len(got.MemoryMarkers) != len(want.MemoryMarkers) || got.MemoryMarkers[0] != want.MemoryMarkers[0] {
		t.Errorf("MemoryMarkers = %v, want %v", got.MemoryMarkers, want.MemoryMarkers)
	}
	if len(got.Decisions) != 1 || got.Decisions[0].Text != want.Decisions[0].Text {
		t.Errorf("Decisions = %v, want %v", got.Decisions, want.Decisions)
	}
	if len(got.OpenQuestions) != 1 || got.OpenQuestions[0] != want.OpenQuestions[0] {
		t.Errorf("OpenQuestions = %v, want %v", got.OpenQuestions, want.OpenQuestions)
	}
	if len(got.Assumptions) != 1 || got.Assumptions[0] != want.Assumptions[0] {
		t.Errorf("Assumptions = %v, want %v", got.Assumptions, want.Assumptions)
	}
}

func TestSave_WritesMarkdown(t *testing.T) {
	s, _ := New(t.TempDir())
	snap := &engine.MemorySnapshot{
		MemoryMarkers: []string{"root cause found"},
		Decisions:     []engine.Decision{{ID: "d-1", Text: "go with L0"}},
	}
	if err := s.Save(snap); err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile(s.MarkdownPath())
	if err != nil {
		t.Fatalf("read markdown: %v", err)
	}
	body := string(md)
	if !strings.Contains(body, "关键发现") || !strings.Contains(body, "root cause found") {
		t.Errorf("markdown missing key findings section:\n%s", body)
	}
	if !strings.Contains(body, "决策记录") || !strings.Contains(body, "go with L0") {
		t.Errorf("markdown missing decisions section:\n%s", body)
	}
}

func TestSave_OverwritesAtomically(t *testing.T) {
	s, _ := New(t.TempDir())
	first := &engine.MemorySnapshot{MemoryMarkers: []string{"a"}}
	second := &engine.MemorySnapshot{MemoryMarkers: []string{"b", "c"}}
	if err := s.Save(first); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(second); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Load()
	if len(got.MemoryMarkers) != 2 || got.MemoryMarkers[0] != "b" {
		t.Errorf("expected overwrite, got %v", got.MemoryMarkers)
	}
}

func TestClear(t *testing.T) {
	s, _ := New(t.TempDir())
	if err := s.Save(&engine.MemorySnapshot{MemoryMarkers: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear(): %v", err)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Errorf("memory.json still exists after Clear")
	}
	if _, err := os.Stat(s.MarkdownPath()); !os.IsNotExist(err) {
		t.Errorf("memory.md still exists after Clear")
	}
}

func TestDefaultDir_IsolatedByCwd(t *testing.T) {
	d1, err := DefaultDir("/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	d2, err := DefaultDir("/proj/b")
	if err != nil {
		t.Fatal(err)
	}
	if d1 == d2 {
		t.Errorf("different cwds must map to different dirs, both %s", d1)
	}
	// Deterministic: same cwd maps to same dir.
	d1b, _ := DefaultDir("/proj/a")
	if d1 != d1b {
		t.Errorf("same cwd must map to same dir: %s vs %s", d1, d1b)
	}
	if !strings.Contains(d1, filepath.Join(".deepact", "memory")) {
		t.Errorf("expected under ~/.deepact/memory, got %s", d1)
	}
}
