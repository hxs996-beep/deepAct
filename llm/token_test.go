package llm

import (
	"testing"
)

func TestTokenEstimator_Estimate_Default(t *testing.T) {
	est := NewTokenEstimator()

	tests := []struct {
		input string
		min   int
		max   int
	}{
		{"", 0, 0},
		{"hi", 1, 1},
		{"hello world this is a test", 5, 12},
		{"func main() { fmt.Println(\"hello\") }", 10, 15},
	}

	for _, tt := range tests {
		got := est.Estimate(tt.input)
		if got < tt.min || got > tt.max {
			t.Errorf("Estimate(%q) = %d, want [%d, %d]", tt.input, got, tt.min, tt.max)
		}
	}
}

func TestTokenEstimator_Calibrate(t *testing.T) {
	est := NewTokenEstimator()

	longText := make([]byte, 300)
	for i := range longText {
		longText[i] = 'a'
	}
	text := string(longText)

	before := est.Estimate(text)

	est.Calibrate(text, Usage{TotalTokens: 150})

	after := est.Estimate(text)

	if after == before {
		t.Error("expected estimate to change after calibration")
	}
	if after != 150 {
		t.Errorf("after calibration: estimate = %d, want 150 (exact)", after)
	}
}

func TestTokenEstimator_Calibrate_IgnoresShortSamples(t *testing.T) {
	est := NewTokenEstimator()

	before := est.Estimate("hello")
	est.Calibrate("hi", Usage{TotalTokens: 100})
	after := est.Estimate("hello")

	if before != after {
		t.Error("short sample should not affect calibration")
	}
}

func TestTokenEstimator_Calibrate_Accumulates(t *testing.T) {
	est := NewTokenEstimator()

	text1 := make([]byte, 300)
	for i := range text1 {
		text1[i] = 'x'
	}
	text2 := make([]byte, 600)
	for i := range text2 {
		text2[i] = 'y'
	}

	est.Calibrate(string(text1), Usage{TotalTokens: 100})
	est.Calibrate(string(text2), Usage{TotalTokens: 200})

	testText := make([]byte, 900)
	for i := range testText {
		testText[i] = 'z'
	}
	got := est.Estimate(string(testText))
	want := 300
	if got != want {
		t.Errorf("accumulated estimate = %d, want %d", got, want)
	}
}
