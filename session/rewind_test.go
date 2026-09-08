package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deepact/deepact/engine"
)

// helperEvents appends n user_message events to sessionID and returns their count.
func appendEvents(t *testing.T, s *Store, sessionID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := s.AppendEvent(engine.Event{
			SessionID: sessionID,
			Type:      "user_message",
			Timestamp: time.Now(),
			Payload:   []byte(`"msg"`),
		}); err != nil {
			t.Fatalf("AppendEvent #%d: %v", i, err)
		}
	}
}

func TestFork_CopiesSessionContent(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(): %v", err)
	}
	appendEvents(t, s, "orig", 3)

	newID, err := s.Fork("orig")
	if err != nil {
		t.Fatalf("Fork(): %v", err)
	}
	if newID == "orig" {
		t.Fatal("Fork must produce a new session ID")
	}
	if !strings.HasPrefix(newID, "orig-") {
		t.Errorf("forked ID = %q, want prefix 'orig-'", newID)
	}

	// Forked session must have identical event content.
	origEvents, err := s.LoadEvents("orig")
	if err != nil {
		t.Fatalf("LoadEvents(orig): %v", err)
	}
	forkEvents, err := s.LoadEvents(newID)
	if err != nil {
		t.Fatalf("LoadEvents(%s): %v", newID, err)
	}
	if len(origEvents) != len(forkEvents) {
		t.Fatalf("fork event count = %d, want %d", len(forkEvents), len(origEvents))
	}
	for i := range origEvents {
		if origEvents[i].Type != forkEvents[i].Type {
			t.Errorf("event[%d] type = %q, want %q", i, forkEvents[i].Type, origEvents[i].Type)
		}
	}
}

func TestFork_NonexistentSession(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	if _, err := s.Fork("does-not-exist"); err == nil {
		t.Fatal("expected error for nonexistent source session")
	}
}

func TestRewind_TruncatesToIndex(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	appendEvents(t, s, "sess", 5)

	if err := s.Rewind("sess", 2); err != nil {
		t.Fatalf("Rewind(): %v", err)
	}
	events, err := s.LoadEvents("sess")
	if err != nil {
		t.Fatalf("LoadEvents(): %v", err)
	}
	if len(events) != 2 {
		t.Errorf("after Rewind(2): %d events, want 2", len(events))
	}
}

func TestRewind_ToZeroClearsFile(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	appendEvents(t, s, "sess", 5)

	if err := s.Rewind("sess", 0); err != nil {
		t.Fatalf("Rewind(0): %v", err)
	}
	events, err := s.LoadEvents("sess")
	if err != nil {
		t.Fatalf("LoadEvents(): %v", err)
	}
	if len(events) != 0 {
		t.Errorf("after Rewind(0): %d events, want 0", len(events))
	}
	// File must still exist (empty), not removed.
	if _, err := os.Stat(filepath.Join(dir, "sess.jsonl")); err != nil {
		t.Errorf("session file should exist after Rewind(0): %v", err)
	}
}

func TestRewind_BeyondEventCountKeepsAll(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	appendEvents(t, s, "sess", 5)

	// toEventIndex beyond available lines: reads everything, rewrites identically.
	if err := s.Rewind("sess", 100); err != nil {
		t.Fatalf("Rewind(100): %v", err)
	}
	events, err := s.LoadEvents("sess")
	if err != nil {
		t.Fatalf("LoadEvents(): %v", err)
	}
	if len(events) != 5 {
		t.Errorf("after Rewind(100): %d events, want 5 (unchanged)", len(events))
	}
}

func TestRewind_NegativeIndex(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	appendEvents(t, s, "sess", 1)

	if err := s.Rewind("sess", -1); err == nil {
		t.Fatal("expected error for negative toEventIndex")
	}
}

func TestRewind_NonexistentSession(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	if err := s.Rewind("nope", 1); err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestRewind_KeepsEventOrder(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	for i := 0; i < 4; i++ {
		if err := s.AppendEvent(engine.Event{
			SessionID: "sess",
			Type:      "user_message",
			Timestamp: time.Now(),
			Payload:   []byte(`"` + strconv.Itoa(i) + `"`),
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	if err := s.Rewind("sess", 2); err != nil {
		t.Fatalf("Rewind(2): %v", err)
	}
	events, _ := s.LoadEvents("sess")
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if string(events[0].Payload) != `"0"` || string(events[1].Payload) != `"1"` {
		t.Errorf("Rewind must keep first N events in order, got %s, %s",
			events[0].Payload, events[1].Payload)
	}
}
