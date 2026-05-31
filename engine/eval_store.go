package engine

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// EvalRecord stores a single evaluation result for later analysis.
type EvalRecord struct {
	ID               string      `json:"id"`
	Timestamp        time.Time   `json:"timestamp"`
	SessionID        string      `json:"session_id"`
	PromptVersion    string      `json:"prompt_version"`
	Phase            string      `json:"phase"`
	TotalScore       float64     `json:"total_score"`
	Passed           bool        `json:"passed"`
	Verdict          string      `json:"verdict"`
	Dimensions       []Dimension `json:"dimensions"`
	Summary          string      `json:"summary"`
	PromptTokens     int         `json:"prompt_tokens"`
	CompletionTokens int         `json:"completion_tokens"`
	IterationCount   int         `json:"iteration_count"`
	TaskComplexity   string      `json:"task_complexity,omitempty"`
	GoalSnippet      string      `json:"goal_snippet,omitempty"`
}

// EvalMetadata carries non-ScoreCard context for an evaluation.
type EvalMetadata struct {
	SessionID        string
	PromptVersion    string
	PromptTokens     int
	CompletionTokens int
	IterationCount   int
	TaskComplexity   string
	GoalSnippet      string
}

// EvalFilter is used to query evaluation records.
type EvalFilter struct {
	Limit     int       // max records to return
	Phase     string    // filter by phase
	Passed    *bool     // filter by pass/fail
	Since     time.Time // only records after this time
	PromptVer string    // filter by prompt version
}

// EvalStats aggregates evaluation records for reporting.
type EvalStats struct {
	TotalRecords   int
	AverageScore   float64
	PassCount      int
	FailCount      int
	PassRate       float64
	ByPhase        map[string]PhaseStats
	ByPromptVer    map[string]PromptVerStats
	AverageTokens  int
	EarliestRecord time.Time
	LatestRecord   time.Time
}

// PhaseStats aggregates scores for a single phase.
type PhaseStats struct {
	Count        int
	AverageScore float64
	PassCount    int
	FailCount    int
}

// PromptVerStats aggregates scores for a single prompt version.
type PromptVerStats struct {
	Count        int
	AverageScore float64
	PassRate     float64
}

// EvalStore persists and queries evaluation records.
type EvalStore interface {
	Insert(record EvalRecord) error
	Query(filter EvalFilter) ([]EvalRecord, error)
	Stats() (*EvalStats, error)
	Close() error
}

// JSONLEvalStore stores evaluation records as JSONL (one JSON per line).
// Zero external dependencies — uses only the standard library.
type JSONLEvalStore struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// NewJSONLEvalStore opens or creates a JSONL file for evaluation records.
func NewJSONLEvalStore(path string) (*JSONLEvalStore, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create eval dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open eval store: %w", err)
	}
	return &JSONLEvalStore{path: path, f: f}, nil
}

func (s *JSONLEvalStore) Insert(record EvalRecord) error {
	if record.ID == "" {
		// Generate a deterministic ID from timestamp + session
		h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", record.SessionID, record.Phase, record.Timestamp.UnixNano())))
		record.ID = fmt.Sprintf("%x", h[:16])
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal eval record: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write eval record: %w", err)
	}
	return nil
}

func (s *JSONLEvalStore) Query(filter EvalFilter) ([]EvalRecord, error) {
	records, err := s.readAll()
	if err != nil {
		return nil, err
	}

	var result []EvalRecord
	for _, r := range records {
		if filter.Phase != "" && r.Phase != filter.Phase {
			continue
		}
		if filter.Passed != nil && r.Passed != *filter.Passed {
			continue
		}
		if !filter.Since.IsZero() && r.Timestamp.Before(filter.Since) {
			continue
		}
		if filter.PromptVer != "" && r.PromptVersion != filter.PromptVer {
			continue
		}
		result = append(result, r)
	}

	// Sort by timestamp descending (newest first)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.After(result[j].Timestamp)
	})

	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}

	return result, nil
}

func (s *JSONLEvalStore) Stats() (*EvalStats, error) {
	records, err := s.readAll()
	if err != nil {
		return nil, err
	}

	stats := &EvalStats{
		ByPhase:     make(map[string]PhaseStats),
		ByPromptVer: make(map[string]PromptVerStats),
	}

	if len(records) == 0 {
		return stats, nil
	}

	stats.TotalRecords = len(records)
	stats.EarliestRecord = records[0].Timestamp
	stats.LatestRecord = records[0].Timestamp

	var totalScore, totalTokens float64
	for _, r := range records {
		totalScore += r.TotalScore
		totalTokens += float64(r.PromptTokens + r.CompletionTokens)
		if r.Passed {
			stats.PassCount++
		} else {
			stats.FailCount++
		}

		// Phase stats
		ps := stats.ByPhase[r.Phase]
		ps.Count++
		ps.AverageScore += r.TotalScore
		if r.Passed {
			ps.PassCount++
		} else {
			ps.FailCount++
		}
		stats.ByPhase[r.Phase] = ps

		// Prompt version stats
		pv := stats.ByPromptVer[r.PromptVersion]
		pv.Count++
		pv.AverageScore += r.TotalScore
		stats.ByPromptVer[r.PromptVersion] = pv

		if r.Timestamp.Before(stats.EarliestRecord) {
			stats.EarliestRecord = r.Timestamp
		}
		if r.Timestamp.After(stats.LatestRecord) {
			stats.LatestRecord = r.Timestamp
		}
	}

	stats.AverageScore = totalScore / float64(len(records))
	stats.AverageTokens = int(totalTokens / float64(len(records)))
	stats.PassRate = float64(stats.PassCount) / float64(len(records)) * 100

	// Finalize phase stats
	for phase, ps := range stats.ByPhase {
		ps.AverageScore = ps.AverageScore / float64(ps.Count)
		stats.ByPhase[phase] = ps
	}

	// Finalize prompt version stats
	for ver, pv := range stats.ByPromptVer {
		pv.AverageScore = pv.AverageScore / float64(pv.Count)
		passCount := 0
		for _, r := range records {
			if r.PromptVersion == ver && r.Passed {
				passCount++
			}
		}
		pv.PassRate = float64(passCount) / float64(pv.Count) * 100
		stats.ByPromptVer[ver] = pv
	}

	return stats, nil
}

func (s *JSONLEvalStore) readAll() ([]EvalRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.f.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seek eval file: %w", err)
	}

	var records []EvalRecord
	scanner := bufio.NewScanner(s.f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec EvalRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // skip corrupt lines
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read eval file: %w", err)
	}

	return records, nil
}

func (s *JSONLEvalStore) Close() error {
	return s.f.Close()
}

// parseScoreFromText extracts total score, pass/fail, and dimensions from
// review text in the format "Total Score: X/100 — PASS/FAIL" and
// "| Dimension | Score | Evidence |".
// Returns a partially populated ScoreCard, suitable for eval recording.
func parseScoreFromText(text string) (score float64, passed bool, verdict string) {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Match "Total Score: 85.0 / 100 — PASS"
		if strings.Contains(trimmed, "Total Score:") || strings.Contains(trimmed, "Total Score :") {
			var s float64
			if _, err := fmt.Sscanf(trimmed, "Total Score: %f", &s); err == nil {
				score = s
			} else if _, err := fmt.Sscanf(trimmed, "Total Score : %f", &s); err == nil {
				score = s
			}
			if strings.Contains(trimmed, "PASS") {
				passed = true
				verdict = "pass"
			} else if strings.Contains(trimmed, "FAIL") {
				passed = false
				verdict = "fail"
			}
		}
	}
	if verdict == "" {
		verdict = "needs_review"
	}
	return
}

// defaultEvalDir returns the default directory for evaluation records.
func defaultEvalDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".deepact", "eval")
	}
	return filepath.Join(os.TempDir(), "deepact", "eval")
}

// defaultEvalPath returns the default path for the eval JSONL file.
func defaultEvalPath() string {
	return filepath.Join(defaultEvalDir(), "records.jsonl")
}
