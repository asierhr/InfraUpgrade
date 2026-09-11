package upgrade

type Recommendation string

type UpgradeDecision struct {
	Recommendation Recommendation
	Reasons        []string
}

const (
	RecommendationSafe         Recommendation = "safe"
	RecommendationManualReview Recommendation = "manual-review"
	RecommendationBlocked      Recommendation = "blocked"
)

func (decision UpgradeDecision) IsSafe() bool {
	return decision.Recommendation == RecommendationSafe
}

func EvaluateRecommendation(report Report, versionChecks []ProviderVersionCheck) UpgradeDecision {
	if !report.Succeeded() {
		return UpgradeDecision{
			Recommendation: RecommendationBlocked,
			Reasons: []string{
				"Terraform validation did not complete successfully",
			},
		}
	}

	if !AllVersionsCheckPassed(versionChecks) {
		return UpgradeDecision{
			Recommendation: RecommendationBlocked,
			Reasons: []string{
				"the selected provider versions do not match the expected version",
			},
		}
	}

	switch report.Comparison.Risk() {
	case "high":
		return evaluateHighRisk(report.Comparison)
	case "medium":
		return evaluateMediumRisk(report.Comparison)
	default:
		return evaluateLowRisk(report.Comparison)
	}
}

func evaluateHighRisk(comparision PlanComparison) UpgradeDecision {
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
		Recommendation: RecommendationBlocked,
		Reasons:        reasons,
	}
}

func evaluateMediumRisk(comparison PlanComparison) UpgradeDecision {
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
		Recommendation: RecommendationManualReview,
		Reasons:        reasons,
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

func evaluateLowRisk(comparison PlanComparison) UpgradeDecision {
	schemaDifferences := countSchemaOnlyDifferences(comparison)

	if schemaDifferences > 0 {
		return UpgradeDecision{
			Recommendation: RecommendationSafe,
			Reasons: []string{
				"only nullable provider schema fields changed",
			},
		}
	}

	return UpgradeDecision{
		Recommendation: RecommendationSafe,
		Reasons: []string{
			"no plan differences were detected",
		},
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
