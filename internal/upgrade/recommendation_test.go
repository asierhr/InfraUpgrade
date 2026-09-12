package upgrade

import "testing"

func TestEvaluateRecommendation(t *testing.T) {
	tests := []struct {
		name       string
		context    StateContext
		comparison PlanComparison
		want       Recommendation
	}{
		{name: "unchanged existing infrastructure", context: StateContextExisting, want: RecommendationSafe},
		{name: "schema-only existing infrastructure", context: StateContextExisting, comparison: PlanComparison{AttributeDifferences: []AttributeDifference{{SchemaOnly: true}}}, want: RecommendationSafe},
		{name: "fresh deployment", context: StateContextFresh, want: RecommendationSafeFresh},
		{name: "unknown state", context: StateContextUnknown, want: RecommendationManualReview},
		{name: "behavioral attribute change", context: StateContextExisting, comparison: PlanComparison{AttributeDifferences: []AttributeDifference{{SchemaOnly: false}}}, want: RecommendationManualReview},
		{name: "action change", context: StateContextExisting, comparison: PlanComparison{Differences: []ActionDifference{{BaselineAction: "no-op", UpgradedAction: "update"}}}, want: RecommendationManualReview},
		{name: "destructive action", context: StateContextExisting, comparison: PlanComparison{Differences: []ActionDifference{{BaselineAction: "no-op", UpgradedAction: "delete"}}}, want: RecommendationBlocked},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := successfulReportForDecision(test.context)
			report.Comparison = test.comparison
			decision := EvaluateRecommendation(report, passingVersionChecks())
			if decision.Recommendation != test.want || decision.ValidationContext != test.context || len(decision.Reasons) == 0 {
				t.Fatalf("EvaluateRecommendation() = %#v; want recommendation %q and context %q", decision, test.want, test.context)
			}
		})
	}
}

func TestEvaluateRecommendationBlocksIncompleteValidation(t *testing.T) {
	report := successfulReportForDecision(StateContextExisting)
	report.Upgraded.PlanAvailable = false
	decision := EvaluateRecommendation(report, passingVersionChecks())
	if decision.Recommendation != RecommendationBlocked {
		t.Fatalf("EvaluateRecommendation() = %#v", decision)
	}
}

func TestEvaluateRecommendationBlocksVersionMismatch(t *testing.T) {
	report := successfulReportForDecision(StateContextExisting)
	checks := passingVersionChecks()
	checks[0].ActualUpgrade = "6.63.0"
	decision := EvaluateRecommendation(report, checks)
	if decision.Recommendation != RecommendationBlocked {
		t.Fatalf("EvaluateRecommendation() = %#v", decision)
	}
}

func TestEvaluateRecommendationBlocksDifferentStateContexts(t *testing.T) {
	report := successfulReportForDecision(StateContextExisting)
	report.Upgraded.Plan.StateContext = StateContextFresh
	decision := EvaluateRecommendation(report, passingVersionChecks())
	if decision.Recommendation != RecommendationBlocked || decision.ValidationContext != StateContextUnknown {
		t.Fatalf("EvaluateRecommendation() = %#v", decision)
	}
}

func TestUpgradeDecisionIsSafe(t *testing.T) {
	if !(UpgradeDecision{Recommendation: RecommendationSafe}).IsSafe() {
		t.Fatal("safe decision was not safe")
	}
	if (UpgradeDecision{Recommendation: RecommendationSafeFresh}).IsSafe() {
		t.Fatal("fresh-deployment decision must not be treated as safe for existing infrastructure")
	}
}

func successfulReportForDecision(context StateContext) Report {
	steps := []StepResult{
		{Name: "init", Required: true},
		{Name: "validate", Required: true},
		{Name: "plan", Required: true},
		{Name: "show", Required: true},
	}
	return Report{
		Baseline:            ExecutionReport{Steps: steps, PlanAvailable: true, Plan: PlanSummary{StateContext: context}},
		Upgraded:            ExecutionReport{Steps: steps, PlanAvailable: true, Plan: PlanSummary{StateContext: context}},
		ComparisonAvailable: true,
	}
}

func passingVersionChecks() []ProviderVersionCheck {
	return []ProviderVersionCheck{{
		Name: "aws", Source: "hashicorp/aws",
		ExpectedBaseline: "6.40.0", ActualBaseline: "6.40.0",
		ExpectedUpgrade: "6.64.0", ActualUpgrade: "6.64.0",
		BaselineFound: true, UpgradeFound: true,
	}}
}
