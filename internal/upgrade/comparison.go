package upgrade

import (
	"fmt"
	"reflect"
	"sort"
)

const (
	AttributeAdded          = "added"
	AttributeRemoved        = "removed"
	AttributeChanged        = "changed"
	AttributeUnknownChanged = "unknown-changed"
)

type ActionDifference struct {
	Address        string
	BaselineAction string
	UpgradedAction string
}

type AttributeDifference struct {
	Address    string
	Phase      string
	Path       string
	Kind       string
	SchemaOnly bool
	Sensitive  bool
}

type PlanComparison struct {
	Differences          []ActionDifference
	AttributeDifferences []AttributeDifference
}

type missingValue struct{}

func (comparison PlanComparison) Risk() string {
	for _, difference := range comparison.Differences {
		if difference.UpgradedAction == "delete" ||
			difference.UpgradedAction == "replace" ||
			difference.UpgradedAction == "absent" {
			return "high"
		}
	}

	if len(comparison.Differences) > 0 {
		return "medium"
	}

	for _, difference := range comparison.AttributeDifferences {
		if !difference.SchemaOnly {
			return "medium"
		}
	}

	return "low"
}

func ComparePlans(baseline PlanSummary, upgraded PlanSummary) PlanComparison {
	addresses := make(map[string]bool)

	for address := range baseline.Actions {
		addresses[address] = true
	}

	for address := range upgraded.Actions {
		addresses[address] = true
	}

	comparison := PlanComparison{}

	for address := range addresses {
		baselineAction := actionForAddress(baseline.Actions, address)

		upgradedAction := actionForAddress(upgraded.Actions, address)

		if baselineAction != upgradedAction {
			comparison.Differences = append(comparison.Differences, ActionDifference{
				Address:        address,
				BaselineAction: baselineAction,
				UpgradedAction: upgradedAction,
			})

			continue
		}

		baselinePlan, baselineExists := baseline.Plans[address]

		upgradedPlan, upgradedExists := upgraded.Plans[address]

		if !baselineExists || !upgradedExists {
			continue
		}

		compareValueTrees(
			address,
			"before",
			"",
			baselinePlan.Before,
			upgradedPlan.Before,
			baselinePlan.BeforeSensitive,
			upgradedPlan.BeforeSensitive,
			&comparison.AttributeDifferences,
		)

		compareValueTrees(
			address,
			"after",
			"",
			baselinePlan.After,
			upgradedPlan.After,
			baselinePlan.AfterSensitive,
			upgradedPlan.AfterSensitive,
			&comparison.AttributeDifferences,
		)

		compareValueTrees(
			address,
			"after_unknown",
			"",
			baselinePlan.AfterUnknown,
			upgradedPlan.AfterUnknown,
			baselinePlan.AfterSensitive,
			upgradedPlan.AfterSensitive,
			&comparison.AttributeDifferences,
		)

		compareValueTrees(
			address,
			"replace_paths",
			"",
			baselinePlan.ReplacePaths,
			upgradedPlan.ReplacePaths,
			nil,
			nil,
			&comparison.AttributeDifferences,
		)
	}

	sort.Slice(comparison.Differences, func(i, j int) bool {
		return comparison.Differences[i].Address < comparison.Differences[j].Address
	})

	sort.Slice(comparison.AttributeDifferences, func(i, j int) bool {
		left := comparison.AttributeDifferences[i]
		right := comparison.AttributeDifferences[j]

		if left.Address != right.Address {
			return left.Address < right.Address
		}

		if left.Phase != right.Phase {
			return left.Phase < right.Phase
		}

		return left.Path < right.Path
	})

	return comparison
}

func actionForAddress(actions map[string]string, address string) string {
	action, exists := actions[address]

	if !exists {
		return "absent"
	}

	return action
}

func compareValueTrees(
	address string,
	phase string,
	path string,
	baseline any,
	upgraded any,
	baselineSensitive any,
	upgradedSensitive any,
	differences *[]AttributeDifference,
) {
	if reflect.DeepEqual(baseline, upgraded) {
		return
	}

	if isSensitive(baselineSensitive) || isSensitive(upgradedSensitive) {
		*differences = append(*differences, AttributeDifference{
			Address:   address,
			Phase:     phase,
			Path:      printablePath(path),
			Sensitive: true,
		})

		return
	}

	baselineObject, baselineIsObject := baseline.(map[string]any)

	upgradedObject, upgradedIsObject := upgraded.(map[string]any)

	if baselineIsObject && upgradedIsObject {
		keys := unionKeys(baselineObject, upgradedObject)

		for _, key := range keys {
			baselineValue, baselineExists := baselineObject[key]
			upgradeValue, upgradeExists := upgradedObject[key]

			if !baselineExists {
				baselineValue = missingValue{}
			}

			if !upgradeExists {
				upgradeValue = missingValue{}
			}

			compareValueTrees(
				address,
				phase,
				appendObjectPath(path, key),
				baselineValue,
				upgradeValue,
				objectChild(baselineSensitive, key),
				objectChild(upgradedSensitive, key),
				differences,
			)
		}

		return
	}

	baselineList, baselineIsList := baseline.([]any)
	upgradedList, upgradedIsList := upgraded.([]any)

	if baselineIsList && upgradedIsList {
		if len(baselineList) != len(upgradedList) {
			appendAttributeDifference(
				address,
				phase,
				path,
				baseline,
				upgraded,
				false,
				differences,
			)

			return
		}

		for index := range baselineList {
			compareValueTrees(
				address,
				phase,
				appendListPath(path, index),
				baselineList[index],
				upgradedList[index],
				listChild(baselineSensitive, index),
				listChild(upgradedSensitive, index),
				differences,
			)
		}

		return
	}

	appendAttributeDifference(
		address,
		phase,
		path,
		baseline,
		upgraded,
		false,
		differences,
	)
}

func unionKeys(left map[string]any, right map[string]any) []string {
	keys := make(map[string]bool)

	for key := range left {
		keys[key] = true
	}

	for key := range right {
		keys[key] = true
	}

	result := make([]string, 0, len(keys))

	for key := range keys {
		result = append(result, key)
	}

	sort.Strings(result)

	return result
}

func appendAttributeDifference(
	address string,
	phase string,
	path string,
	baseline any,
	upgrade any,
	sensitive bool,
	differences *[]AttributeDifference,
) {
	kind, schemaOnly := classifyAttributedDifference(phase, baseline, upgrade)

	*differences = append(*differences, AttributeDifference{
		Address:    address,
		Phase:      phase,
		Path:       printablePath(path),
		Kind:       kind,
		SchemaOnly: schemaOnly,
		Sensitive:  sensitive,
	})
}

func classifyAttributedDifference(phase string, baseline any, upgrade any) (kind string, schemaOnly bool) {
	baselineMissing := isMissingValue(baseline)
	upgradedMissing := isMissingValue(upgrade)

	switch {
	case baselineMissing && upgrade == nil:
		return AttributeAdded, true

	case upgradedMissing && baseline == nil:
		return AttributeRemoved, true

	case phase == "after_unknown":
		return AttributeUnknownChanged, false

	case baselineMissing:
		return AttributeAdded, false

	case upgradedMissing:
		return AttributeRemoved, false

	default:
		return AttributeChanged, false
	}
}

func isMissingValue(value any) bool {
	_, missing := value.(missingValue)

	return missing
}

func objectChild(value any, key string) any {
	object, ok := value.(map[string]any)

	if !ok {
		return nil
	}

	return object[key]
}

func listChild(value any, index int) any {
	list, ok := value.([]any)

	if !ok || index >= len(list) {
		return nil
	}

	return list[index]
}

func isSensitive(value any) bool {
	sensitive, ok := value.(bool)

	return ok && sensitive
}

func appendObjectPath(path string, key string) string {
	if path == "" {
		return key
	}

	return path + "." + key
}

func appendListPath(path string, index int) string {
	return fmt.Sprintf("%s[%d]", path, index)
}

func printablePath(path string) string {
	if path == "" {
		return "(root)"
	}

	return path
}
