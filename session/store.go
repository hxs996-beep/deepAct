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

type Store struct {
	dir string
}

type SessionInfo struct {
	ID         string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	EventCount int
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
		created, updated, count, err := sessionStats(path)
		if err != nil {
			return nil, err
		}
		if created.IsZero() {
			created = info.ModTime()
		}
		if updated.IsZero() {
			updated = info.ModTime()
		}
		infos = append(infos, SessionInfo{ID: id, CreatedAt: created, UpdatedAt: updated, EventCount: count})
	}
	return infos, nil
}

func (s *Store) sessionPath(sessionID string) string {
	return filepath.Join(s.dir, sessionID+".jsonl")
}

func sessionStats(path string) (time.Time, time.Time, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("open session file: %w", err)
	}
	defer file.Close()

	var created time.Time
	var updated time.Time
	count := 0

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		count++
		var event engine.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return time.Time{}, time.Time{}, 0, fmt.Errorf("unmarshal event: %w", err)
		}
		if created.IsZero() || event.Timestamp.Before(created) {
			created = event.Timestamp
		}
		if updated.IsZero() || event.Timestamp.After(updated) {
			updated = event.Timestamp
		}
	}
	if err := scanner.Err(); err != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("read session file: %w", err)
	}
	return created, updated, count, nil
}
