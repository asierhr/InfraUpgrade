package upgrade

type Recommendation string

type UpgradeDecision struct {
	Recommendation    Recommendation
	ValidationContext StateContext
	Reasons           []string
}

const (
	RecommendationSafe         Recommendation = "safe"
	RecommendationSafeFresh    Recommendation = "safe-and-fresh-deployment"
	RecommendationManualReview Recommendation = "manual-review"
	RecommendationBlocked      Recommendation = "blocked"
)

func (decision UpgradeDecision) IsSafe() bool {
	return decision.Recommendation == RecommendationSafe
}

func EvaluateRecommendation(report Report, versionChecks []ProviderVersionCheck) UpgradeDecision {

	stateContext := report.Baseline.Plan.StateContext

	if !report.Succeeded() {
		return UpgradeDecision{
			Recommendation:    RecommendationBlocked,
			ValidationContext: stateContext,
			Reasons: []string{
				"Terraform validation did not complete successfully",
			},
		}
	}

	if !AllVersionsCheckPassed(versionChecks) {
		return UpgradeDecision{
			Recommendation:    RecommendationBlocked,
			ValidationContext: stateContext,
			Reasons: []string{
				"the selected provider versions do not match the expected version",
			},
		}
	}

	if report.Baseline.Plan.StateContext != report.Upgraded.Plan.StateContext {
		return UpgradeDecision{
			Recommendation:    RecommendationBlocked,
			ValidationContext: StateContextUnknown,
			Reasons: []string{
				"the baseline and upgraded plans used different state contexts",
			},
		}
	}

	switch report.Comparison.Risk() {
	case "high":
		return evaluateHighRisk(report.Comparison, stateContext)
	case "medium":
		return evaluateMediumRisk(report.Comparison, stateContext)
	default:
		return evaluateLowRisk(report.Comparison, stateContext)
	}
}

func evaluateHighRisk(comparision PlanComparison, stateContext StateContext) UpgradeDecision {
	reasons := make([]string, 0)

	destructiveDifferences := 0

	for _, difference := range comparision.Differences {
		if difference.UpgradedAction == "delete" || difference.UpgradedAction == "replace" || difference.UpgradedAction == "absent" {
			destructiveDifferences++
		}
	}

	if destructiveDifferences > 0 {
		reasons = append(reasons, "the upgraded plan introduces destructives action differences")
	}

	if len(reasons) == 0 {
		reasons = append(reasons, "the upgraded plan has high-risk differences")
	}

	return UpgradeDecision{
		Recommendation:    RecommendationBlocked,
		ValidationContext: stateContext,
		Reasons:           reasons,
	}
}

func evaluateMediumRisk(comparison PlanComparison, stateContext StateContext) UpgradeDecision {
	reasons := make([]string, 0)

	if len(comparison.Differences) > 0 {
		reasons = append(reasons, "resource actions differ between the baseline and upgraded plan")
	}

	behavioralDifferences := countBehavioralAttributeDifferences(comparison)

	if behavioralDifferences > 0 {
		reasons = append(reasons, "planned resources attributes differ between the baseline and upgraded plans")
	}

	if len(reasons) == 0 {
		reasons = append(reasons, "the upgrade contains differences that require manual review")
	}

	return UpgradeDecision{
		Recommendation:    RecommendationManualReview,
		ValidationContext: stateContext,
		Reasons:           reasons,
	}
}

func countBehavioralAttributeDifferences(comparison PlanComparison) int {
	count := 0

	for _, difference := range comparison.AttributeDifferences {
		if !difference.SchemaOnly {
			count++
		}
	}

	return count
}

func evaluateLowRisk(comparison PlanComparison, stateContext StateContext) UpgradeDecision {
	schemaDifferences := countSchemaOnlyDifferences(comparison)

	switch stateContext {
	case StateContextExisting:
		reasons := make([]string, 0)

		if schemaDifferences > 0 {
			reasons = append(reasons, "only nullable provider schema fields changed")
		} else {
			reasons = append(reasons, "no plan differences were detected")
		}

		return UpgradeDecision{
			Recommendation:    RecommendationSafe,
			ValidationContext: stateContext,
			Reasons:           reasons,
		}

	case StateContextFresh:
		reasons := []string{
			"the upgrade was validated against an empty state",
			"existing infrastructure was not evaluated",
		}

		if schemaDifferences > 0 {
			reasons = append(reasons, "only nullable schema fiels changed")

		}

		return UpgradeDecision{
			Recommendation:    RecommendationSafeFresh,
			ValidationContext: stateContext,
			Reasons:           reasons,
		}
	default:
		return UpgradeDecision{
			Recommendation:    RecommendationManualReview,
			ValidationContext: stateContext,
			Reasons: []string{
				"the Terraform state context could not be determined",
			},
		}
	}
}

func countSchemaOnlyDifferences(comparison PlanComparison) int {
	count := 0

	for _, difference := range comparison.AttributeDifferences {
		if difference.SchemaOnly {
			count++
		}
	}

	return count
}
