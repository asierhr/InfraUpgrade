package migration

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/asierhr/infraupgrade/internal/schemadiff"
)

func TestBuildReturnsUnsupportedWhenRemovedAttributeHasNoReplacement(t *testing.T) {
	report := plannerReport(
		plannerAssignment(nil, "old_name"),
		schemadiff.AttributeRemoved,
		plannerResource(map[string]schemadiff.AttributeSchema{
			"old_name": plannerAttribute(`"string"`, true, false, false),
		}, nil),
		plannerResource(nil, nil),
	)

	plan := Build(report)
	proposal := requireSingleProposal(t, plan)

	if proposal.Status != ProposalUnsupported {
		t.Fatalf("Build() status = %q; want %q", proposal.Status, ProposalUnsupported)
	}
	if len(proposal.Candidates) != 0 {
		t.Fatalf("Build() candidates = %v; want none", proposal.Candidates)
	}
	if proposal.Selected != nil {
		t.Fatalf("Build() selected = %v; want nil", proposal.Selected)
	}
}

func TestBuildSelectsSingleRenameCandidate(t *testing.T) {
	report := plannerReport(
		plannerAssignment(nil, "old_name"),
		schemadiff.AttributeRemoved,
		plannerResource(map[string]schemadiff.AttributeSchema{
			"old_name": plannerAttribute(`"string"`, true, false, false),
		}, nil),
		plannerResource(map[string]schemadiff.AttributeSchema{
			"new_name": plannerAttribute(`"string"`, false, true, false),
		}, nil),
	)

	proposal := requireSingleProposal(t, Build(report))

	if proposal.Status != ProposalCandidate {
		t.Fatalf("Build() status = %q; want %q", proposal.Status, ProposalCandidate)
	}
	if proposal.Selected == nil {
		t.Fatal("Build() selected = nil; want the only candidate")
	}
	if proposal.Selected.ToPath != "new_name" {
		t.Fatalf("Build() selected path = %q; want %q", proposal.Selected.ToPath, "new_name")
	}
	wantTransformations := []TransformationKind{RenameAttribute}
	if !reflect.DeepEqual(proposal.Selected.Transformation, wantTransformations) {
		t.Fatalf("Build() transformations = %v; want %v", proposal.Selected.Transformation, wantTransformations)
	}
	if proposal.Selected.BeforeType != `"string"` || proposal.Selected.AfterType != `"string"` {
		t.Fatalf("Build() types = %s -> %s; want string -> string", proposal.Selected.BeforeType, proposal.Selected.AfterType)
	}
}

func TestBuildMarksMultipleCompatibleReplacementsAsAmbiguous(t *testing.T) {
	report := plannerReport(
		plannerAssignment(nil, "old_name"),
		schemadiff.AttributeRemoved,
		plannerResource(map[string]schemadiff.AttributeSchema{
			"old_name": plannerAttribute(`"string"`, true, false, false),
		}, nil),
		plannerResource(map[string]schemadiff.AttributeSchema{
			"first_name":  plannerAttribute(`"string"`, false, true, false),
			"second_name": plannerAttribute(`"string"`, false, true, false),
		}, nil),
	)

	proposal := requireSingleProposal(t, Build(report))

	if proposal.Status != ProposalAmbiguous {
		t.Fatalf("Build() status = %q; want %q", proposal.Status, ProposalAmbiguous)
	}
	if proposal.Selected != nil {
		t.Fatalf("Build() selected = %v; want nil for an ambiguous proposal", proposal.Selected)
	}
	gotPaths := []string{proposal.Candidates[0].ToPath, proposal.Candidates[1].ToPath}
	wantPaths := []string{"first_name", "second_name"}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("Build() candidate paths = %v; want %v", gotPaths, wantPaths)
	}
}

func TestBuildInfersCollectionWrappers(t *testing.T) {
	tests := []struct {
		name           string
		afterType      string
		transformation TransformationKind
	}{
		{name: "list", afterType: `["list","string"]`, transformation: WrapInList},
		{name: "set", afterType: `["set","string"]`, transformation: WrapInSet},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := plannerReport(
				plannerAssignment(nil, "old_value"),
				schemadiff.AttributeRemoved,
				plannerResource(map[string]schemadiff.AttributeSchema{
					"old_value": plannerAttribute(`"string"`, true, false, false),
				}, nil),
				plannerResource(map[string]schemadiff.AttributeSchema{
					"new_value": plannerAttribute(test.afterType, false, true, false),
				}, nil),
			)

			proposal := requireSingleProposal(t, Build(report))
			if proposal.Status != ProposalCandidate || proposal.Selected == nil {
				t.Fatalf("Build() proposal = %+v; want selected candidate", proposal)
			}

			want := []TransformationKind{RenameAttribute, test.transformation}
			if !reflect.DeepEqual(proposal.Selected.Transformation, want) {
				t.Fatalf("Build() transformations = %v; want %v", proposal.Selected.Transformation, want)
			}
		})
	}
}

func TestBuildInfersMoveToNestedBlock(t *testing.T) {
	report := plannerReport(
		plannerAssignment(nil, "value"),
		schemadiff.AttributeRemoved,
		plannerResource(map[string]schemadiff.AttributeSchema{
			"value": plannerAttribute(`"string"`, true, false, false),
		}, nil),
		plannerResource(nil, map[string]schemadiff.NestedBlockSchema{
			"settings": plannerNestedBlock(map[string]schemadiff.AttributeSchema{
				"value": plannerAttribute(`"string"`, false, true, false),
			}),
		}),
	)

	proposal := requireSingleProposal(t, Build(report))
	if proposal.Status != ProposalCandidate || proposal.Selected == nil {
		t.Fatalf("Build() proposal = %+v; want selected candidate", proposal)
	}
	if proposal.Selected.ToPath != "settings.value" {
		t.Fatalf("Build() selected path = %q; want %q", proposal.Selected.ToPath, "settings.value")
	}
	want := []TransformationKind{MoveAttribute}
	if !reflect.DeepEqual(proposal.Selected.Transformation, want) {
		t.Fatalf("Build() transformations = %v; want %v", proposal.Selected.Transformation, want)
	}
}

func TestBuildIgnoresAddedOptionalAttributeWithoutAffectedAssignment(t *testing.T) {
	report := schemadiff.Report{
		Assigments: []schemadiff.Assignment{plannerAssignment(nil, "existing")},
		Baseline: schemadiff.Snapshot{Resources: map[string]schemadiff.ResourceSchema{
			"example_resource": plannerResource(map[string]schemadiff.AttributeSchema{
				"existing": plannerAttribute(`"string"`, false, true, false),
			}, nil),
		}},
		Upgraded: schemadiff.Snapshot{Resources: map[string]schemadiff.ResourceSchema{
			"example_resource": plannerResource(map[string]schemadiff.AttributeSchema{
				"existing":  plannerAttribute(`"string"`, false, true, false),
				"new_field": plannerAttribute(`"string"`, false, true, false),
			}, nil),
		}},
	}

	plan := Build(report)
	if len(plan.Proposals) != 0 {
		t.Fatalf("Build() proposals = %v; want none", plan.Proposals)
	}
}

func TestBuildRejectsComputedOnlyAndIncompatibleDestinations(t *testing.T) {
	report := plannerReport(
		plannerAssignment(nil, "old_value"),
		schemadiff.AttributeRemoved,
		plannerResource(map[string]schemadiff.AttributeSchema{
			"old_value": plannerAttribute(`"string"`, true, false, false),
		}, nil),
		plannerResource(map[string]schemadiff.AttributeSchema{
			"computed_value": plannerAttribute(`"string"`, false, false, true),
			"number_value":   plannerAttribute(`"number"`, false, true, false),
		}, nil),
	)

	proposal := requireSingleProposal(t, Build(report))
	if proposal.Status != ProposalUnsupported {
		t.Fatalf("Build() status = %q; want %q", proposal.Status, ProposalUnsupported)
	}
	if len(proposal.Candidates) != 0 {
		t.Fatalf("Build() candidates = %v; want none", proposal.Candidates)
	}
}

func plannerReport(assignment schemadiff.Assignment, kind schemadiff.ChangeKind, baseline schemadiff.ResourceSchema, upgraded schemadiff.ResourceSchema) schemadiff.Report {
	return schemadiff.Report{
		Assigments: []schemadiff.Assignment{assignment},
		Changes: []schemadiff.AssigmentChange{{
			Assignment: assignment,
			Kind:       kind,
		}},
		Baseline: schemadiff.Snapshot{Resources: map[string]schemadiff.ResourceSchema{
			assignment.ResourceType: baseline,
		}},
		Upgraded: schemadiff.Snapshot{Resources: map[string]schemadiff.ResourceSchema{
			assignment.ResourceType: upgraded,
		}},
	}
}

func plannerAssignment(blockPath []string, attribute string) schemadiff.Assignment {
	return schemadiff.Assignment{
		File:         "main.tf",
		ResourceType: "example_resource",
		ResourceName: "example",
		BlockPath:    blockPath,
		Attribute:    attribute,
		Expression:   `"value"`,
	}
}

func plannerResource(attributes map[string]schemadiff.AttributeSchema, blockTypes map[string]schemadiff.NestedBlockSchema) schemadiff.ResourceSchema {
	return schemadiff.ResourceSchema{
		Provider:   "registry.example/example",
		Attributes: attributes,
		BlockTypes: blockTypes,
	}
}

func plannerNestedBlock(attributes map[string]schemadiff.AttributeSchema) schemadiff.NestedBlockSchema {
	return schemadiff.NestedBlockSchema{
		NestingMode: "list",
		Block: schemadiff.BlockSchema{
			Attributes: attributes,
		},
	}
}

func plannerAttribute(attributeType string, required bool, optional bool, computed bool) schemadiff.AttributeSchema {
	return schemadiff.AttributeSchema{
		Type:     json.RawMessage(attributeType),
		Required: required,
		Optional: optional,
		Computed: computed,
	}
}

func requireSingleProposal(t *testing.T, plan Plan) Proposal {
	t.Helper()

	if len(plan.Proposals) != 1 {
		t.Fatalf("Build() proposal count = %d; want 1", len(plan.Proposals))
	}

	return plan.Proposals[0]
}
