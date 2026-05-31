package llm

import "sync"

type TokenEstimator struct {
	mu           sync.Mutex
	calibrated   bool
	avgPerChar   float64
	minSample    int
	sampleTokens int
	sampleChars  int
}

func NewTokenEstimator() *TokenEstimator {
	return &TokenEstimator{
		avgPerChar: 1.0 / 3.0,
		minSample:  200,
	}
}

func (t *TokenEstimator) Estimate(text string) int {
	if text == "" {
		return 0
	}
	avg := t.avg()
	est := int(float64(len(text)) * avg)
	if est < 1 {
		return 1
	}
	return est
}

func (t *TokenEstimator) Calibrate(text string, usage Usage) {
	if text == "" || usage.TotalTokens <= 0 {
		return
	}
	chars := len(text)
	if chars == 0 {
		return
	}
	if chars < t.minSample {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sampleTokens += usage.TotalTokens
	t.sampleChars += chars
	t.avgPerChar = float64(t.sampleTokens) / float64(t.sampleChars)
	t.calibrated = true
}

func (t *TokenEstimator) avg() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.calibrated {
		return t.avgPerChar
	}
	return 1.0 / 3.0
}
