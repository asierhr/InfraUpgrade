package upgrade

import "testing"

func TestParsePlanToJSONCountsActions(t *testing.T) {
	content := []byte(`{
		"resource_changes": [
			{"address":"a.create","mode":"managed","change":{"actions":["create"]}},
			{"address":"a.update","mode":"managed","change":{"actions":["update"]}},
			{"address":"a.delete","mode":"managed","change":{"actions":["delete"]}},
			{"address":"a.replace","mode":"managed","change":{"actions":["create","delete"]}},
			{"address":"a.read","mode":"data","change":{"actions":["read"]}},
			{"address":"a.noop","mode":"managed","change":{"actions":["no-op"]}},
			{"address":"a.unknown","mode":"managed","change":{"actions":[]}}
		]
	}`)

	summary, err := ParsePlanToJSON(content)
	if err != nil {
		t.Fatalf("ParsePlanToJSON() error = %v", err)
	}

	if summary.Create != 1 || summary.Update != 1 || summary.Delete != 1 ||
		summary.Replace != 1 || summary.Read != 1 || summary.NoOp != 1 || summary.Unknown != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Actions["a.noop"] != "no-op" || summary.Actions["a.unknown"] != "unknown" {
		t.Fatalf("actions = %#v", summary.Actions)
	}
	if len(summary.Changes) != 6 {
		t.Fatalf("len(Changes) = %d, want 6", len(summary.Changes))
	}
	if summary.Risk() != "high" {
		t.Fatalf("Risk() = %q, want high", summary.Risk())
	}
}

func TestParsePlanToJSONRejectsInvalidJSON(t *testing.T) {
	if _, err := ParsePlanToJSON([]byte(`{"resource_changes":`)); err == nil {
		t.Fatal("ParsePlanToJSON() error = nil")
	}
}

func TestPlanRisk(t *testing.T) {
	tests := []struct {
		name    string
		summary PlanSummary
		want    string
	}{
		{name: "low", summary: PlanSummary{}, want: "low"},
		{name: "medium create", summary: PlanSummary{Create: 1}, want: "medium"},
		{name: "medium update", summary: PlanSummary{Update: 1}, want: "medium"},
		{name: "high delete", summary: PlanSummary{Delete: 1}, want: "high"},
		{name: "high replace", summary: PlanSummary{Replace: 1}, want: "high"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.summary.Risk(); got != test.want {
				t.Fatalf("Risk() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClassifyActions(t *testing.T) {
	tests := []struct {
		name    string
		actions []string
		want    string
	}{
		{name: "create", actions: []string{"create"}, want: "create"},
		{name: "replacement delete first", actions: []string{"delete", "create"}, want: "replace"},
		{name: "replacement create first", actions: []string{"create", "delete"}, want: "replace"},
		{name: "empty", actions: nil, want: "unknown"},
		{name: "unsupported", actions: []string{"forget"}, want: "unknown"},
		{name: "multiple unsupported", actions: []string{"read", "update"}, want: "unknown"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyActions(test.actions); got != test.want {
				t.Fatalf("classifyActions() = %q, want %q", got, test.want)
			}
		})
	}
}
