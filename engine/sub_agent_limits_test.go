package engine

import (
	"strings"
	"testing"
)

// A refused delegation is NOT a partial result: nothing ran. These two reasons
// carry their own headings so the parent reads "refused, continue" rather than
// the "partial result" wording every run-that-stopped-halfway reason uses.
func TestHandoffReasonHeading_RefusalReasons(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{HandoffReasonFanoutLimit, "refused"},
		{HandoffReasonTreeBudgetExceeded, "refused"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			got := handoffReasonHeading(tc.reason, "", false)
			if !strings.Contains(strings.ToLower(got), tc.want) {
				t.Errorf("heading for %q must say %q, got %q", tc.reason, tc.want, got)
			}
			// A refusal must never claim a partial result: that wording tells
			// the parent to salvage an outcome that does not exist.
			if strings.Contains(strings.ToLower(got), "partial") {
				t.Errorf("heading for %q must not say \"partial\", got %q", tc.reason, got)
			}
		})
	}
}

// A refused delegation needs the parent to continue, exactly like a run that
// hit its turn cap — so both reasons must be follow-up reasons.
func TestIsHandoffFollowUpReason_Refusals(t *testing.T) {
	if !isHandoffFollowUpReason(HandoffReasonFanoutLimit) {
		t.Error("fanout_limit must be a follow-up reason")
	}
	if !isHandoffFollowUpReason(HandoffReasonTreeBudgetExceeded) {
		t.Error("tree_budget_exceeded must be a follow-up reason")
	}
}
