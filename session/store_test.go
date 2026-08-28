package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// TestListFirstMsg verifies List() populates FirstMsg from the first user message.
func TestListFirstMsg(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(): %v", err)
	}
	// user_message 事件 + message 事件
	s.AppendEvent(engine.Event{SessionID: "sess1", Type: "user_message", Timestamp: time.Now(),
		Payload: json.RawMessage(`"第一条用户消息，这是一段较长的内容需要截断"`)})
	s.AppendEvent(engine.Event{SessionID: "sess1", Type: engine.EventTypeMessage, Timestamp: time.Now(),
		Payload: json.RawMessage(`{"role":"assistant","content":"回答"}`)})

	infos, err := s.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("len(infos) = %d, want 1", len(infos))
	}
	if !strings.Contains(infos[0].FirstMsg, "第一条用户消息") {
		t.Errorf("FirstMsg = %q, want contains 第一条用户消息", infos[0].FirstMsg)
	}
	if len([]rune(infos[0].FirstMsg)) > 43 {
		t.Errorf("FirstMsg = %q, want truncated to <=40 runes + ellipsis", infos[0].FirstMsg)
	}
}

// TestListFirstMsg_TruncatesLongUserMessage verifies a >40-rune user_message
// payload is truncated to 40 runes plus an ellipsis.
func TestListFirstMsg_TruncatesLongUserMessage(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(): %v", err)
	}
	long := strings.Repeat("这段内容足够长，用来验证会话预览的省略号截断逻辑是否真正生效。", 4)
	s.AppendEvent(engine.Event{SessionID: "sess1", Type: "user_message", Timestamp: time.Now(),
		Payload: json.RawMessage(`"` + long + `"`)})

	infos, err := s.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("len(infos) = %d, want 1", len(infos))
	}
	got := infos[0].FirstMsg
	if r := []rune(got); len(r) != firstMsgMaxRunes+1 {
		t.Errorf("FirstMsg runes = %d, want %d (40 + ellipsis); got %q", len(r), firstMsgMaxRunes+1, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("FirstMsg = %q, want suffix …", got)
	}
}

// TestListFirstMsg_FallbackToMessageEvent verifies the message-event fallback:
// a session with only message events (role=user) still populates FirstMsg,
// and the fallback content is truncated like the user_message branch.
func TestListFirstMsg_FallbackToMessageEvent(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(): %v", err)
	}
	long := strings.Repeat("这是 message 事件兜底分支的消息内容，足够长以验证兜底分支同样截断。", 3)
	s.AppendEvent(engine.Event{SessionID: "sess1", Type: engine.EventTypeMessage, Timestamp: time.Now(),
		Payload: json.RawMessage(`{"role":"user","content":"` + long + `"}`)})

	infos, err := s.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("len(infos) = %d, want 1", len(infos))
	}
	if !strings.Contains(infos[0].FirstMsg, "这是 message 事件兜底分支的消息内容") {
		t.Errorf("FirstMsg = %q, want contains 兜底消息内容", infos[0].FirstMsg)
	}
	if r := []rune(infos[0].FirstMsg); len(r) != firstMsgMaxRunes+1 {
		t.Errorf("FirstMsg runes = %d, want %d (fallback should truncate too); got %q", len(r), firstMsgMaxRunes+1, infos[0].FirstMsg)
	}
	if !strings.HasSuffix(infos[0].FirstMsg, "…") {
		t.Errorf("FirstMsg = %q, want suffix …", infos[0].FirstMsg)
	}
}

// TestListFirstMsg_NonStringPayloadFallback verifies extractFirstMsg returns ""
// for a non-string payload (e.g. "{}") and the message-event fallback picks up.
func TestListFirstMsg_NonStringPayloadFallback(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(): %v", err)
	}
	s.AppendEvent(engine.Event{SessionID: "sess1", Type: "user_message", Timestamp: time.Now(),
		Payload: json.RawMessage(`{}`)})
	s.AppendEvent(engine.Event{SessionID: "sess1", Type: engine.EventTypeMessage, Timestamp: time.Now(),
		Payload: json.RawMessage(`{"role":"user","content":"兜底取到这条消息"}`)})

	infos, err := s.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("len(infos) = %d, want 1", len(infos))
	}
	if !strings.Contains(infos[0].FirstMsg, "兜底取到这条消息") {
		t.Errorf("FirstMsg = %q, want contains 兜底取到这条消息", infos[0].FirstMsg)
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
