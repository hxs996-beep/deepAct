// Package artifact provides content-addressable storage (CAS) for large tool outputs.
// Artifacts are stored by SHA256 hash under ~/.deepact/artifacts/<prefix>/<hash>.
// Reference format: "sha256:<hex>" where <hex> is the full SHA256 digest.
package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// PermDir is the directory permission for artifact storage.
	PermDir = 0o755
	// PermFile is the file permission for stored artifacts.
	PermFile = 0o644
)

// Store provides content-addressable storage for artifacts.
type Store struct {
	baseDir string
}

// ArtifactInfo contains metadata about a stored artifact.
type ArtifactInfo struct {
	Ref       string    `json:"ref"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// New creates a new Store rooted at baseDir.
// baseDir is typically ~/.deepact/artifacts.
func New(baseDir string) (*Store, error) {
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("artifact: resolve base dir: %w", err)
	}
	if err := os.MkdirAll(abs, PermDir); err != nil {
		return nil, fmt.Errorf("artifact: create base dir: %w", err)
	}
	return &Store{baseDir: abs}, nil
}

// Store saves data as a content-addressed artifact and returns its reference.
// The reference format is "sha256:<hex>".
func (s *Store) Store(data []byte) (string, error) {
	hash := sha256.Sum256(data)
	hexHash := hex.EncodeToString(hash[:])
	ref := fmt.Sprintf("sha256:%s", hexHash)

	// Check if already stored (content-addressable = dedup by content)
	if s.exists(hexHash) {
		return ref, nil
	}

	relPath := filepath.Join(hexHash[:2], hexHash)
	fullPath := filepath.Join(s.baseDir, relPath)

	if err := os.MkdirAll(filepath.Dir(fullPath), PermDir); err != nil {
		return "", fmt.Errorf("artifact: create dir for %s: %w", ref, err)
	}

	if err := os.WriteFile(fullPath, data, PermFile); err != nil {
		return "", fmt.Errorf("artifact: write %s: %w", ref, err)
	}

	return ref, nil
}

// Load retrieves artifact data by reference.
// The ref must be in "sha256:<hex>" format.
func (s *Store) Load(ref string) ([]byte, error) {
	hexHash, err := parseRef(ref)
	if err != nil {
		return nil, err
	}

	relPath := filepath.Join(hexHash[:2], hexHash)
	fullPath := filepath.Join(s.baseDir, relPath)

	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("artifact %s: not found", ref)
		}
		return nil, fmt.Errorf("artifact: read %s: %w", ref, err)
	}

	return data, nil
}

// Exists checks whether an artifact with the given reference exists.
func (s *Store) Exists(ref string) bool {
	hexHash, err := parseRef(ref)
	if err != nil {
		return false
	}
	return s.exists(hexHash)
}

// Delete removes an artifact by reference.
// Returns nil if the artifact doesn't exist (idempotent).
func (s *Store) Delete(ref string) error {
	hexHash, err := parseRef(ref)
	if err != nil {
		return err
	}

	relPath := filepath.Join(hexHash[:2], hexHash)
	fullPath := filepath.Join(s.baseDir, relPath)

	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("artifact: delete %s: %w", ref, err)
	}
	return nil
}

// List returns metadata for all stored artifacts, sorted by creation time (newest first).
func (s *Store) List() ([]ArtifactInfo, error) {
	var artifacts []ArtifactInfo

	err := filepath.Walk(s.baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible paths
		}
		if info.IsDir() || len(info.Name()) != 64 {
			return nil // not a sha256 hex file
		}
		// Verify it's a valid hex hash
		if _, err := hex.DecodeString(info.Name()); err != nil {
			return nil
		}
		ref := fmt.Sprintf("sha256:%s", info.Name())
		artifacts = append(artifacts, ArtifactInfo{
			Ref:       ref,
			Size:      info.Size(),
			CreatedAt: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("artifact: list: %w", err)
	}

	sort.Slice(artifacts, func(i, j int) bool {
		return artifacts[i].CreatedAt.After(artifacts[j].CreatedAt)
	})
	return artifacts, nil
}

// StoreReader saves all data read from r as a content-addressed artifact.
func (s *Store) StoreReader(r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("artifact: read input: %w", err)
	}
	return s.Store(data)
}

// BaseDir returns the root directory of the store.
func (s *Store) BaseDir() string {
	return s.baseDir
}

func (s *Store) exists(hexHash string) bool {
	relPath := filepath.Join(hexHash[:2], hexHash)
	fullPath := filepath.Join(s.baseDir, relPath)
	_, err := os.Stat(fullPath)
	return err == nil
}

func parseRef(ref string) (string, error) {
	if !strings.HasPrefix(ref, "sha256:") || len(ref) != 64+7 {
		return "", fmt.Errorf("invalid artifact ref: %q (expected sha256:<64 hex chars>)", ref)
	}
	hexHash := ref[7:]
	if len(hexHash) != 64 {
		return "", fmt.Errorf("invalid artifact ref hash length: %d", len(hexHash))
	}
	if _, err := hex.DecodeString(hexHash); err != nil {
		return "", fmt.Errorf("invalid artifact ref hex: %w", err)
	}
	return hexHash, nil
}
