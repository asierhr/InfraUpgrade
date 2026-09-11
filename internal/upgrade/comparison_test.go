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

func TestComparePlansClassifiesAttributeDifferences(t *testing.T) {
	tests := []struct {
		name           string
		phase          string
		baseline       map[string]any
		upgraded       map[string]any
		wantPath       string
		wantKind       string
		wantSchemaOnly bool
		wantRisk       string
	}{
		{
			name:           "missing to null is schema only",
			phase:          "after",
			baseline:       map[string]any{},
			upgraded:       map[string]any{"new_field": nil},
			wantPath:       "new_field",
			wantKind:       AttributeAdded,
			wantSchemaOnly: true,
			wantRisk:       "low",
		},
		{
			name:           "null to missing is schema only",
			phase:          "after",
			baseline:       map[string]any{"old_field": nil},
			upgraded:       map[string]any{},
			wantPath:       "old_field",
			wantKind:       AttributeRemoved,
			wantSchemaOnly: true,
			wantRisk:       "low",
		},
		{
			name:     "non null attribute is added",
			phase:    "after",
			baseline: map[string]any{},
			upgraded: map[string]any{"enabled": true},
			wantPath: "enabled",
			wantKind: AttributeAdded,
			wantRisk: "medium",
		},
		{
			name:     "attribute is removed",
			phase:    "after",
			baseline: map[string]any{"enabled": true},
			upgraded: map[string]any{},
			wantPath: "enabled",
			wantKind: AttributeRemoved,
			wantRisk: "medium",
		},
		{
			name:     "attribute value changes",
			phase:    "after",
			baseline: map[string]any{"size": "small"},
			upgraded: map[string]any{"size": "large"},
			wantPath: "size",
			wantKind: AttributeChanged,
			wantRisk: "medium",
		},
		{
			name:     "unknown status changes",
			phase:    "after_unknown",
			baseline: map[string]any{"id": true},
			upgraded: map[string]any{"id": false},
			wantPath: "id",
			wantKind: AttributeUnknownChanged,
			wantRisk: "medium",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baselinePlan := ResourcePlan{}
			upgradedPlan := ResourcePlan{}
			if test.phase == "after_unknown" {
				baselinePlan.AfterUnknown = test.baseline
				upgradedPlan.AfterUnknown = test.upgraded
			} else {
				baselinePlan.After = test.baseline
				upgradedPlan.After = test.upgraded
			}

			baseline := PlanSummary{
				Actions: map[string]string{"test.resource": "update"},
				Plans:   map[string]ResourcePlan{"test.resource": baselinePlan},
			}
			upgraded := PlanSummary{
				Actions: map[string]string{"test.resource": "update"},
				Plans:   map[string]ResourcePlan{"test.resource": upgradedPlan},
			}

			comparison := ComparePlans(baseline, upgraded)
			if len(comparison.AttributeDifferences) != 1 {
				t.Fatalf("attribute differences = %#v, want one", comparison.AttributeDifferences)
			}

			difference := comparison.AttributeDifferences[0]
			if difference.Path != test.wantPath || difference.Kind != test.wantKind || difference.SchemaOnly != test.wantSchemaOnly {
				t.Fatalf("difference = %#v, want path %q, kind %q, schemaOnly %t", difference, test.wantPath, test.wantKind, test.wantSchemaOnly)
			}
			if comparison.Risk() != test.wantRisk {
				t.Fatalf("Risk() = %q, want %q", comparison.Risk(), test.wantRisk)
			}
		})
	}
}

func TestComparePlansFindsNestedAndListDifferencesInStableOrder(t *testing.T) {
	baselinePlan := ResourcePlan{After: map[string]any{
		"tags":  map[string]any{"Name": "old"},
		"ports": []any{float64(80), float64(443)},
	}}
	upgradedPlan := ResourcePlan{After: map[string]any{
		"tags":  map[string]any{"Name": "new"},
		"ports": []any{float64(80), float64(8443)},
	}}

	comparison := ComparePlans(
		PlanSummary{
			Actions: map[string]string{"test.resource": "update"},
			Plans:   map[string]ResourcePlan{"test.resource": baselinePlan},
		},
		PlanSummary{
			Actions: map[string]string{"test.resource": "update"},
			Plans:   map[string]ResourcePlan{"test.resource": upgradedPlan},
		},
	)

	if len(comparison.AttributeDifferences) != 2 {
		t.Fatalf("attribute differences = %#v, want two", comparison.AttributeDifferences)
	}
	if comparison.AttributeDifferences[0].Path != "ports[1]" || comparison.AttributeDifferences[1].Path != "tags.Name" {
		t.Fatalf("attribute differences are not sorted: %#v", comparison.AttributeDifferences)
	}
}

func TestComparePlansDoesNotExposeSensitiveValue(t *testing.T) {
	baselinePlan := ResourcePlan{
		After:          map[string]any{"password": "old-secret"},
		AfterSensitive: map[string]any{"password": true},
	}
	upgradedPlan := ResourcePlan{
		After:          map[string]any{"password": "new-secret"},
		AfterSensitive: map[string]any{"password": true},
	}

	comparison := ComparePlans(
		PlanSummary{
			Actions: map[string]string{"test.resource": "update"},
			Plans:   map[string]ResourcePlan{"test.resource": baselinePlan},
		},
		PlanSummary{
			Actions: map[string]string{"test.resource": "update"},
			Plans:   map[string]ResourcePlan{"test.resource": upgradedPlan},
		},
	)

	if len(comparison.AttributeDifferences) != 1 {
		t.Fatalf("attribute differences = %#v, want one", comparison.AttributeDifferences)
	}
	difference := comparison.AttributeDifferences[0]
	if !difference.Sensitive || difference.Path != "password" {
		t.Fatalf("difference = %#v, want sensitive password", difference)
	}
	if difference.Kind != AttributeChanged {
		t.Fatalf("Kind = %q, want %q", difference.Kind, AttributeChanged)
	}
}

func TestComparePlansReportsListLengthAndRootDifferences(t *testing.T) {
	var differences []AttributeDifference
	compareValueTrees("test.list", "after", "items", []any{1.0}, []any{1.0, 2.0}, nil, nil, &differences)
	compareValueTrees("test.root", "after", "", "old", "new", nil, nil, &differences)

	if len(differences) != 2 {
		t.Fatalf("differences = %#v, want two", differences)
	}
	if differences[0].Path != "items" || differences[1].Path != "(root)" {
		t.Fatalf("differences = %#v", differences)
	}
}
