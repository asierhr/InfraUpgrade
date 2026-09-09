package upgrade

import "testing"

func TestComparePlans(t *testing.T) {
	baseline := PlanSummary{Actions: map[string]string{
		"a.same":    "create",
		"a.changed": "no-op",
		"a.removed": "no-op",
	}}
	upgraded := PlanSummary{Actions: map[string]string{
		"a.same":    "create",
		"a.changed": "replace",
		"a.added":   "update",
	}}

	comparison := ComparePlans(baseline, upgraded)
	if len(comparison.Differences) != 3 {
		t.Fatalf("len(Differences) = %d, want 3", len(comparison.Differences))
	}
	if comparison.Differences[0].Address != "a.added" ||
		comparison.Differences[1].Address != "a.changed" ||
		comparison.Differences[2].Address != "a.removed" {
		t.Fatalf("differences are not sorted: %#v", comparison.Differences)
	}
	if comparison.Differences[0].BaselineAction != "absent" {
		t.Errorf("added baseline action = %q", comparison.Differences[0].BaselineAction)
	}
	if comparison.Differences[2].UpgradedAction != "absent" {
		t.Errorf("removed upgraded action = %q", comparison.Differences[2].UpgradedAction)
	}
	if comparison.Risk() != "high" {
		t.Fatalf("Risk() = %q, want high", comparison.Risk())
	}
}

func TestCompareIdenticalPlansHasLowRisk(t *testing.T) {
	actions := map[string]string{"aws_instance.web": "create"}
	comparison := ComparePlans(
		PlanSummary{Actions: actions},
		PlanSummary{Actions: actions},
	)

	if len(comparison.Differences) != 0 || comparison.Risk() != "low" {
		t.Fatalf("comparison = %#v, risk = %q", comparison, comparison.Risk())
	}
}

func TestComparisonWithNonDestructiveDifferenceHasMediumRisk(t *testing.T) {
	comparison := ComparePlans(
		PlanSummary{Actions: map[string]string{"a.resource": "no-op"}},
		PlanSummary{Actions: map[string]string{"a.resource": "update"}},
	)

	if comparison.Risk() != "medium" {
		t.Fatalf("Risk() = %q, want medium", comparison.Risk())
	}
}
