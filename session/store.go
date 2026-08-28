package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deepact/deepact/engine"
)

// firstMsgMaxRunes 是会话预览首条 user 消息的最大 rune 数（截断上限）。
const firstMsgMaxRunes = 40

type Store struct {
	dir string
}

type SessionInfo struct {
	ID         string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	EventCount int
	FirstMsg   string // 首条 user 消息摘要（会话预览，≤firstMsgMaxRunes rune）
}

func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("session dir is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) AppendEvent(event engine.Event) error {
	path := s.sessionPath(event.SessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open session file: %w", err)
	}
	defer file.Close()

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

func (s *Store) LoadEvents(sessionID string) ([]engine.Event, error) {
	path := s.sessionPath(sessionID)
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session file: %w", err)
	}
	defer file.Close()

	var events []engine.Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event engine.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("unmarshal event: %w", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}
	return events, nil
}

func (s *Store) List() ([]SessionInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read session dir: %w", err)
	}
	infos := make([]SessionInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("stat session file: %w", err)
		}
		id := strings.TrimSuffix(name, ".jsonl")
		path := filepath.Join(s.dir, name)
		created, updated, count, firstMsg, err := sessionStats(path)
		if err != nil {
			return nil, err
		}
		if created.IsZero() {
			created = info.ModTime()
		}
		if updated.IsZero() {
			updated = info.ModTime()
		}
		infos = append(infos, SessionInfo{ID: id, CreatedAt: created, UpdatedAt: updated, EventCount: count, FirstMsg: firstMsg})
	}
	return infos, nil
}

func (s *Store) sessionPath(sessionID string) string {
	return filepath.Join(s.dir, sessionID+".jsonl")
}

func sessionStats(path string) (time.Time, time.Time, int, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return time.Time{}, time.Time{}, 0, "", fmt.Errorf("open session file: %w", err)
	}
	defer file.Close()

	var created time.Time
	var updated time.Time
	count := 0
	var firstMsg string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		count++
		var event engine.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return time.Time{}, time.Time{}, 0, "", fmt.Errorf("unmarshal event: %w", err)
		}
		if created.IsZero() || event.Timestamp.Before(created) {
			created = event.Timestamp
		}
		if updated.IsZero() || event.Timestamp.After(updated) {
			updated = event.Timestamp
		}
		// 提取首条 user 消息作为会话预览（user_message 事件优先，message 事件兜底）
		if firstMsg == "" && event.Type == "user_message" {
			firstMsg = extractFirstMsg(event.Payload)
		}
		if firstMsg == "" && event.Type == engine.EventTypeMessage && event.Payload != nil {
			var m engine.Message
			if err := json.Unmarshal(event.Payload, &m); err == nil && m.Role == "user" && strings.TrimSpace(m.Content) != "" {
				firstMsg = truncateRunes(strings.TrimSpace(m.Content), firstMsgMaxRunes)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return time.Time{}, time.Time{}, 0, "", fmt.Errorf("read session file: %w", err)
	}
	return created, updated, count, firstMsg, nil
}

// extractFirstMsg 提取 user_message 事件的 payload 文本并截断到 firstMsgMaxRunes rune。
func extractFirstMsg(payload json.RawMessage) string {
	var s string
	if err := json.Unmarshal(payload, &s); err != nil {
		return ""
	}
	return truncateRunes(strings.TrimSpace(s), firstMsgMaxRunes)
}

// truncateRunes 按 rune 截断字符串，超长追加省略号。
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
