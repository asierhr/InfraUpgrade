package upgrade

import "sort"

type ActionDifference struct {
	Address        string
	BaselineAction string
	UpgradedAction string
}

type PlanComparison struct {
	Differences []ActionDifference
}

func (comparison PlanComparison) Risk() string {
	if len(comparison.Differences) == 0 {
		return "low"
	}

	for _, difference := range comparison.Differences {
		if difference.UpgradedAction == "delete" ||
			difference.UpgradedAction == "replace" ||
			difference.UpgradedAction == "absent" {
			return "high"
		}
	}

	return "medium"
}

func ComparePlans(baseline PlanSummary, upgraded PlanSummary) PlanComparison {
	addresses := make(map[string]bool)

	for address := range baseline.Actions {
		addresses[address] = true
	}

	for address := range upgraded.Actions {
		addresses[address] = true
	}

	var differences []ActionDifference

	for address := range addresses {
		baselineAction := actionForAddress(baseline.Actions, address)

		upgradedAction := actionForAddress(upgraded.Actions, address)

		if baselineAction == upgradedAction {
			continue
		}

		differences = append(differences, ActionDifference{
			Address:        address,
			BaselineAction: baselineAction,
			UpgradedAction: upgradedAction,
		})
	}

	sort.Slice(differences, func(i, j int) bool {
		return differences[i].Address < differences[j].Address
	})

	return PlanComparison{
		Differences: differences,
	}
}

func actionForAddress(actions map[string]string, address string) string {
	action, exists := actions[address]

	if !exists {
		return "absent"
	}

	return action
}
