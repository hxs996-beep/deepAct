// Package memory provides cross-session persistent memory for DeepAct.
//
// Memory is stored per-project under ~/.deepact/memory/<cwd-hash>/ and survives
// process restarts, unlike session-scoped TaskState (which lives and dies with
// the process). The engine loads the snapshot at startup, merges it into
// TaskState (so it flows into Block B automatically), and saves it back after
// each completed turn.
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deepact/deepact/engine"
)

const (
	// MemoryFileName is the machine-readable snapshot file.
	MemoryFileName = "memory.json"
	// MarkdownFileName is the human-readable rendering of the snapshot.
	MarkdownFileName = "memory.md"

	permDir  os.FileMode = 0o755
	permFile os.FileMode = 0o644
)

// Store persists memory snapshots as JSON on disk.
type Store struct {
	dir string
}

// New creates a Store rooted at dir, creating the directory if needed.
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("memory: dir is required")
	}
	if err := os.MkdirAll(dir, permDir); err != nil {
		return nil, fmt.Errorf("memory: create dir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// DefaultDir computes the per-project memory directory for cwd:
// ~/.deepact/memory/<first-4-bytes-sha256(cwd)>. The hash keeps the path
// filesystem-safe (spaces, CJK, "~") and stable across sessions on the same
// project while isolating different projects from each other.
func DefaultDir(cwd string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("memory: home dir: %w", err)
	}
	sum := sha256.Sum256([]byte(cwd))
	hash := hex.EncodeToString(sum[:4])
	return filepath.Join(home, ".deepact", "memory", hash), nil
}

// Dir returns the storage directory.
func (s *Store) Dir() string { return s.dir }

// Path returns the memory.json path.
func (s *Store) Path() string { return filepath.Join(s.dir, MemoryFileName) }

// MarkdownPath returns the memory.md path.
func (s *Store) MarkdownPath() string { return filepath.Join(s.dir, MarkdownFileName) }

// Load reads the persisted memory snapshot. Returns (nil, nil) when no memory
// has been saved yet.
func (s *Store) Load() (*engine.MemorySnapshot, error) {
	data, err := os.ReadFile(s.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory: read %s: %w", s.Path(), err)
	}
	var snap engine.MemorySnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("memory: decode %s: %w", s.Path(), err)
	}
	return &snap, nil
}

// Save atomically writes the snapshot to memory.json and refreshes the
// human-readable memory.md. Both use tmp + rename so a crash mid-write never
// leaves a half-written file.
func (s *Store) Save(snap *engine.MemorySnapshot) error {
	if snap == nil {
		return nil
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("memory: marshal: %w", err)
	}
	if err := s.writeFileAtomic(s.Path(), data); err != nil {
		return fmt.Errorf("memory: write json: %w", err)
	}
	if err := s.writeFileAtomic(s.MarkdownPath(), []byte(RenderMarkdown(snap))); err != nil {
		return fmt.Errorf("memory: write markdown: %w", err)
	}
	return nil
}

// Clear removes the persisted memory files for this project.
func (s *Store) Clear() error {
	for _, p := range []string{s.Path(), s.MarkdownPath()} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("memory: remove %s: %w", p, err)
		}
	}
	return nil
}

// writeFileAtomic writes data to path via a temp file + rename.
func (s *Store) writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, permFile); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
