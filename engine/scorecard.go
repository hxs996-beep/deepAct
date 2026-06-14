package engine

import (
	"encoding/json"
	"fmt"
	"time"
)

// ScoreCard is used by Challenger and Tester to evaluate proposals, plans, and code.
type ScoreCard struct {
	Phase      string      `json:"phase"`
	Dimensions []Dimension `json:"dimensions"`
	TotalScore float64     `json:"total_score"` // 0–100
	Passed     bool        `json:"passed"`      // TotalScore >= 80
	Verdict    string      `json:"verdict"`     // "pass" | "fail" | "needs_review"
	Summary    string      `json:"summary"`
}

// Dimension is a single scoring criterion.
type Dimension struct {
	Name        string  `json:"name"`        // e.g. "Code Relevance", "Plan Completeness"
	Score       float64 `json:"score"`       // 0–100
	Weight      float64 `json:"weight"`      // 0.0–1.0, sum of all weights = 1.0
	Evidence    string  `json:"evidence"`    // what supports this score
	Issue       string  `json:"issue"`       // what's wrong, if any
	Improvement string  `json:"improvement"` // suggested fix
}

const scorePassThreshold = 80.0

// ComputeTotal calculates the weighted total score and sets Passed.
func (s *ScoreCard) ComputeTotal() {
	var total, weightSum float64
	for _, d := range s.Dimensions {
		total += d.Score * d.Weight
		weightSum += d.Weight
	}
	if weightSum > 0 {
		s.TotalScore = total / weightSum
	} else {
		s.TotalScore = 0
	}
	s.Passed = s.TotalScore >= scorePassThreshold
	if s.Passed {
		s.Verdict = "pass"
	} else {
		s.Verdict = "fail"
	}
}

// ToJSON serializes the ScoreCard to JSON bytes.
func (s *ScoreCard) ToJSON() ([]byte, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshal scorecard: %w", err)
	}
	return data, nil
}

// ToEvalRecord converts the ScoreCard and metadata into an EvalRecord for persistence.
func (s *ScoreCard) ToEvalRecord(meta EvalMetadata) EvalRecord {
	rec := EvalRecord{
		Timestamp:        time.Now(),
		SessionID:        meta.SessionID,
		PromptVersion:    meta.PromptVersion,
		Phase:            s.Phase,
		TotalScore:       s.TotalScore,
		Passed:           s.Passed,
		Verdict:          s.Verdict,
		Dimensions:       s.Dimensions,
		Summary:          s.Summary,
		PromptTokens:     meta.PromptTokens,
		CompletionTokens: meta.CompletionTokens,
		IterationCount:   meta.IterationCount,
		TaskComplexity:   meta.TaskComplexity,
		GoalSnippet:      meta.GoalSnippet,
	}
	return rec
}

// PersistTo saves the ScoreCard to an EvalStore with the given metadata.
func (s *ScoreCard) PersistTo(store EvalStore, meta EvalMetadata) error {
	rec := s.ToEvalRecord(meta)
	return store.Insert(rec)
}

// FormatScoreCard produces a human-readable summary of the score card.
func FormatScoreCard(card *ScoreCard) string {
	s := fmt.Sprintf("## Score Card — %s\n\n", card.Phase)
	s += fmt.Sprintf("**Total Score:** %.1f / 100\n", card.TotalScore)
	s += fmt.Sprintf("**Verdict:** %s\n\n", card.Verdict)
	s += "| Dimension | Score | Weight | Evidence | Issue |\n"
	s += "|-----------|-------|--------|----------|-------|\n"
	for _, d := range card.Dimensions {
		issue := d.Issue
		if issue == "" {
			issue = "—"
		}
		s += fmt.Sprintf("| %s | %.0f | %.0f%% | %s | %s |\n", d.Name, d.Score, d.Weight*100, d.Evidence, issue)
	}
	if card.Summary != "" {
		s += fmt.Sprintf("\n**Summary:** %s\n", card.Summary)
	}
	return s
}
