package session

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func (s *Store) Fork(sessionID string) (string, error) {
	src := s.sessionPath(sessionID)
	newID := fmt.Sprintf("%s-%d", sessionID, time.Now().UnixNano())
	dst := s.sessionPath(newID)

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}
	from, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("open source session: %w", err)
	}
	defer from.Close()

	to, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("create session file: %w", err)
	}
	defer to.Close()

	if _, err := io.Copy(to, from); err != nil {
		return "", fmt.Errorf("copy session: %w", err)
	}
	return newID, nil
}

func (s *Store) Rewind(sessionID string, toEventIndex int) error {
	if toEventIndex < 0 {
		return fmt.Errorf("invalid event index: %d", toEventIndex)
	}
	path := s.sessionPath(sessionID)
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open session file: %w", err)
	}
	defer file.Close()

	lines := make([]string, 0, toEventIndex)
	scanner := bufio.NewScanner(file)
	count := 0
	for scanner.Scan() {
		if count >= toEventIndex {
			break
		}
		lines = append(lines, scanner.Text())
		count++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read session file: %w", err)
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("close session file: %w", err)
	}

	writeFile, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("truncate session file: %w", err)
	}
	defer writeFile.Close()

	writer := bufio.NewWriter(writeFile)
	for _, line := range lines {
		if _, err := writer.WriteString(line + "\n"); err != nil {
			return fmt.Errorf("write session file: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush session file: %w", err)
	}
	return nil
}
