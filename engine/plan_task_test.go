package engine

import (
	"strings"
	"testing"
)

func TestPlanMethodology_NonEmptyBothLanguages(t *testing.T) {
	zh := planMethodology(true)
	en := planMethodology(false)
	if strings.TrimSpace(zh) == "" {
		t.Error("zh methodology should not be empty")
	}
	if strings.TrimSpace(en) == "" {
		t.Error("en methodology should not be empty")
	}
	if zh == en {
		t.Error("zh and en methodologies should differ")
	}
	if !strings.Contains(zh, "吃透背景") || !strings.Contains(zh, "多假设") {
		t.Error("zh methodology should cover core steps (吃透背景 / 多假设)")
	}
	if !strings.Contains(en, "background") || !strings.Contains(en, "hypotheses") {
		t.Error("en methodology should cover core steps (background / hypotheses)")
	}
}
