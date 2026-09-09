package upgrade

import (
	"encoding/json"
	"fmt"
)

type PlannedResourceChange struct {
	Address string
	Mode    string
	Action  string
}

type PlanSummary struct {
	Create  int
	Update  int
	Delete  int
	Replace int
	Read    int
	NoOp    int
	Unknown int

	Changes []PlannedResourceChange
	Actions map[string]string
}

type planDocument struct {
	ResourceChanges []struct {
		Address string `json:"address"`
		Mode    string `json:"mode"`

		Change struct {
			Actions []string `json:"actions"`
		} `json:"change"`
	} `json:"resource_changes"`
}

func (summary PlanSummary) Risk() string {
	switch {
	case summary.Delete > 0 || summary.Replace > 0:
		return "high"
	case summary.Update > 0 || summary.Create > 0:
		return "medium"
	default:
		return "low"
	}
}

func ParsePlanToJSON(content []byte) (PlanSummary, error) {
	var document planDocument

	if err := json.Unmarshal(content, &document); err != nil {
		return PlanSummary{}, fmt.Errorf("decode Terraform plan JSON: %w", err)
	}

	summary := PlanSummary{
		Actions: make(map[string]string),
	}

	for _, resource := range document.ResourceChanges {
		action := classifyActions(resource.Change.Actions)

		summary.Actions[resource.Address] = action

		switch action {
		case "create":
			summary.Create++

		case "update":
			summary.Update++

		case "delete":
			summary.Delete++

		case "replace":
			summary.Replace++

		case "read":
			summary.Read++

		case "no-op":
			summary.NoOp++

		default:
			summary.Unknown++
		}

		if action != "no-op" {
			summary.Changes = append(summary.Changes, PlannedResourceChange{
				Address: resource.Address,
				Mode:    resource.Mode,
				Action:  action,
			})
		}
	}

	return summary, nil
}

func classifyActions(actions []string) string {
	if containsAction(actions, "create") && containsAction(actions, "delete") {
		return "replace"
	}

	if len(actions) != 1 {
		return "unknown"
	}

	switch actions[0] {
	case "create":
		return "create"

	case "update":
		return "update"

	case "delete":
		return "delete"

	case "read":
		return "read"

	case "no-op":
		return "no-op"

	default:
		return "unknown"
	}
}

func containsAction(actions []string, expected string) bool {
	for _, action := range actions {
		if action == expected {
			return true
		}
	}

	return false
}
