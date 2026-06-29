package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deepact/deepact/engine"
)

func TestNewStore(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore() unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil Store")
	}
}

func TestNewStore_EmptyDir(t *testing.T) {
	_, err := NewStore("")
	if err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestAppendEvent(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(): %v", err)
	}

	event := engine.Event{
		SessionID: "test-session",
		Type:      "user_message",
		Timestamp: time.Now(),
	}
	if err := s.AppendEvent(event); err != nil {
		t.Fatalf("AppendEvent(): %v", err)
	}

	// Verify file exists
	path := filepath.Join(dir, "test-session.jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("session file not created")
	}
}

func TestAppendEvent_MultipleEvents(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)

	for i := 0; i < 5; i++ {
		event := engine.Event{
			SessionID: "multi",
			Type:      "event",
			Timestamp: time.Now(),
		}
		if err := s.AppendEvent(event); err != nil {
			t.Fatalf("AppendEvent #%d: %v", i, err)
		}
	}

	events, err := s.LoadEvents("multi")
	if err != nil {
		t.Fatalf("LoadEvents(): %v", err)
	}
	if len(events) != 5 {
		t.Errorf("expected 5 events, got %d", len(events))
	}
}

func TestLoadEvents_NotFound(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)

	_, err := s.LoadEvents("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestLoadEvents_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)

	// Create an empty file
	path := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatalf("create empty file: %v", err)
	}

	events, err := s.LoadEvents("empty")
	if err != nil {
		t.Fatalf("LoadEvents(): %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events from empty file, got %d", len(events))
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)

	// No sessions yet
	infos, err := s.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(infos))
	}

	// Add a session
	event := engine.Event{
		SessionID: "sess1",
		Type:      "test",
		Timestamp: time.Now(),
	}
	s.AppendEvent(event)

	infos, err = s.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 session, got %d", len(infos))
	}
	if infos[0].ID != "sess1" {
		t.Errorf("session ID = %q, want 'sess1'", infos[0].ID)
	}
	if infos[0].EventCount != 1 {
		t.Errorf("EventCount = %d, want 1", infos[0].EventCount)
	}
}

func TestList_IgnoresNonJSONL(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)

	// Create non-JSONL files
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(dir, "data.log"), []byte("content"), 0o644)

	s.AppendEvent(engine.Event{SessionID: "sess1", Type: "test", Timestamp: time.Now()})

	infos, err := s.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(infos) != 1 {
		t.Errorf("expected 1 session (ignoring non-jsonl), got %d", len(infos))
	}
}

func TestList_IgnoresDirectories(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)

	os.MkdirAll(filepath.Join(dir, "subdir"), 0o755)
	s.AppendEvent(engine.Event{SessionID: "sess1", Type: "test", Timestamp: time.Now()})

	infos, err := s.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(infos) != 1 {
		t.Errorf("expected 1 session (ignoring dirs), got %d", len(infos))
	}
}

func TestSessionPath(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	path := s.sessionPath("my-session")
	want := filepath.Join(dir, "my-session.jsonl")
	if path != want {
		t.Errorf("sessionPath = %q, want %q", path, want)
	}
}
