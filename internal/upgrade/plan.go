package upgrade

import (
	"encoding/json"
	"fmt"
)

type StateContext string

type priorState struct {
	Values *stateValues `json:"values"`
}

type stateValues struct {
	RootModule *stateModule `json:"root_module"`
}

type stateModule struct {
	Resources    []stateResource `json:"resources"`
	ChildModules []stateModule   `json:"child_modules"`
}

type stateResource struct {
	Mode string `json:"mode"`
}

type PlannedResourceChange struct {
	Address string
	Mode    string
	Action  string
}

type ResourcePlan struct {
	Before          any
	After           any
	AfterUnknown    any
	BeforeSensitive any
	AfterSensitive  any
	ReplacePaths    any
}

type PlanSummary struct {
	Create  int
	Update  int
	Delete  int
	Replace int
	Read    int
	NoOp    int
	Unknown int

	StateContext         StateContext
	PriorManagedResource int

	Changes []PlannedResourceChange
	Actions map[string]string
	Plans   map[string]ResourcePlan
}

type planDocument struct {
	PriorState *priorState `json:"prior_state"`

	ResourceChanges []struct {
		Address string `json:"address"`
		Mode    string `json:"mode"`

		Change struct {
			Actions         []string `json:"actions"`
			Before          any      `json:"before"`
			After           any      `json:"after"`
			AfterUnknown    any      `json:"after_unknown"`
			BeforeSensitive any      `json:"before_sensitive"`
			AfterSensitive  any      `json:"after_sensitive"`
			ReplacePaths    any      `json:"replace_paths"`
		} `json:"change"`
	} `json:"resource_changes"`
}

const (
	StateContextExisting StateContext = "existing-infrastructure"
	StateContextFresh    StateContext = "fresh-deployment"
	StateContextUnknown  StateContext = "unknown"
)

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
		Plans:   make(map[string]ResourcePlan),
	}

	summary.StateContext, summary.PriorManagedResource = detectStateContext(document)

	for _, resource := range document.ResourceChanges {
		action := classifyActions(resource.Change.Actions)

		summary.Actions[resource.Address] = action

		summary.Plans[resource.Address] = ResourcePlan{
			Before:          resource.Change.Before,
			After:           resource.Change.After,
			AfterUnknown:    resource.Change.AfterUnknown,
			BeforeSensitive: resource.Change.BeforeSensitive,
			AfterSensitive:  resource.Change.AfterSensitive,
			ReplacePaths:    resource.Change.ReplacePaths,
		}

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

func detectStateContext(document planDocument) (StateContext, int) {
	if document.PriorState != nil {
		count := 0

		if document.PriorState.Values != nil {
			count = countManagedResources(document.PriorState.Values.RootModule)
		}

		if count > 0 {
			return StateContextExisting, count
		}

		return StateContextFresh, 0
	}

	managedResources := 0
	resourcesWithPreviousState := 0

	for _, resource := range document.ResourceChanges {
		if resource.Mode != "managed" {
			continue
		}

		managedResources++

		if resource.Change.Before != nil {
			resourcesWithPreviousState++
		}
	}

	if resourcesWithPreviousState > 0 {
		return StateContextFresh, 0
	}

	return StateContextUnknown, 0
}

func countManagedResources(module *stateModule) int {
	if module == nil {
		return 0
	}

	count := 0

	for _, resource := range module.Resources {
		if resource.Mode == "managed" {
			count++
		}
	}

	for index := range module.ChildModules {
		count += countManagedResources(&module.ChildModules[index])
	}

	return count
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
