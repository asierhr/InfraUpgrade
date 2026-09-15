package migration

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/asierhr/infraupgrade/internal/schemadiff"
)

type TransformationKind string

const (
	RenameAttribute TransformationKind = "rename-attribute"
	MoveAttribute   TransformationKind = "move-attribute"
	WrapInList      TransformationKind = "wrap-in-list"
	WrapInSet       TransformationKind = "wrap-in-set"
	ManualChange    TransformationKind = "manual-change"
)

type ProposalStatus string

const (
	ProposalUnsupported ProposalStatus = "unsupported"
	ProposalAmbiguous   ProposalStatus = "ambiguous"
	ProposalCandidate   ProposalStatus = "candidate"
	ProposalVerified    ProposalStatus = "verified"
	ProposalRejected    ProposalStatus = "rejected"
)

type Candidate struct {
	ToPath         string
	Transformation []TransformationKind
	Evidence       []string
	BeforeType     string
	AfterType      string
}

type Proposal struct {
	Assignment     schemadiff.Assignment
	DetectedChange schemadiff.ChangeKind
	Status         ProposalStatus
	Candidates     []Candidate
	Selected       *Candidate
	Verification   *Verification
}

type Plan struct {
	Proposals []Proposal
}

type schemaAttribute struct {
	Path      string
	Parent    string
	Name      string
	Attribute schemadiff.AttributeSchema
}

type Verification struct {
	Reason string
	Risk   string
}

func Build(report schemadiff.Report) Plan {
	plan := Plan{}

	for _, change := range report.Changes {
		if change.Kind != schemadiff.AttributeRemoved && change.Kind != schemadiff.NestedBlockRemoved {
			continue
		}

		proposal := buildProposal(change, report.Baseline, report.Upgraded)

		plan.Proposals = append(plan.Proposals, proposal)
	}

	sort.Slice(plan.Proposals, func(i, j int) bool {
		left := plan.Proposals[i].Assignment
		right := plan.Proposals[j].Assignment

		if left.File != right.File {
			return left.File < right.File
		}

		if left.ResourceType != right.ResourceType {
			return left.ResourceType < right.ResourceType
		}

		if left.ResourceName != right.ResourceName {
			return left.ResourceName < right.ResourceName
		}

		return left.FullPath() < right.FullPath()
	})

	return plan
}

func buildProposal(change schemadiff.AssigmentChange, baseline schemadiff.Snapshot, upgraded schemadiff.Snapshot) Proposal {
	proposal := Proposal{
		Assignment:     change.Assignment,
		DetectedChange: change.Kind,
		Status:         ProposalUnsupported,
	}

	baselineResource, baselineExists := baseline.Resources[change.Assignment.ResourceType]

	upgradedResource, upgradedExists := upgraded.Resources[change.Assignment.ResourceType]

	if !baselineExists || !upgradedExists {
		return proposal
	}

	baselineAttributes := flattenResource(baselineResource)
	upgradedAttributes := flattenResource(upgradedResource)

	addedAttributes := findAddedAttributes(baselineAttributes, upgradedAttributes)

	source, sourceExists := baselineAttributes[change.Assignment.FullPath()]

	if !sourceExists {
		return proposal
	}

	for _, destination := range addedAttributes {
		candidate, compatible := buildCandidate(source, destination)

		if !compatible {
			continue
		}

		proposal.Candidates = append(proposal.Candidates, candidate)
	}

	sortCandidates(proposal.Candidates)

	switch len(proposal.Candidates) {
	case 0:
		proposal.Status = ProposalUnsupported
	case 1:
		proposal.Status = ProposalCandidate

		selected := proposal.Candidates[0]

		proposal.Selected = &selected
	default:
		proposal.Status = ProposalAmbiguous
	}

	return proposal
}

func buildCandidate(source schemaAttribute, destination schemaAttribute) (Candidate, bool) {
	if !isConfigurable(destination.Attribute) {
		return Candidate{}, false
	}

	if !isStructurallyPlausible(source, destination) {
		return Candidate{}, false
	}

	beforeType := typeSignature(source.Attribute.Type)

	afterType := typeSignature(destination.Attribute.Type)

	typeTransformation, typeEvidence, compatible := inferTypeTransformation(beforeType, afterType)

	if !compatible {
		return Candidate{}, false
	}

	transformations := make([]TransformationKind, 0, len(typeTransformation)+1)

	evidence := make([]string, 0, len(typeEvidence)+3)

	if source.Parent == destination.Parent {
		transformations = append(transformations, RenameAttribute)

		evidence = append(evidence, "the source and destination belong to the same block")
	} else {
		transformations = append(transformations, MoveAttribute)
		evidence = append(evidence, "the source and destination use the same attribute name in different blocks")
	}

	transformations = append(transformations, typeTransformation...)

	evidence = append(evidence, typeEvidence...)

	evidence = append(evidence, "the destination attribute is configurable")

	return Candidate{
		ToPath:         destination.Path,
		Transformation: transformations,
		Evidence:       evidence,
		BeforeType:     beforeType,
		AfterType:      afterType,
	}, true
}

func isStructurallyPlausible(source schemaAttribute, destination schemaAttribute) bool {
	if source.Parent == destination.Parent {
		return true
	}

	if source.Name == destination.Name {
		return true
	}

	return false
}

func inferTypeTransformation(beforeType string, afterType string) ([]TransformationKind, []string, bool) {
	switch {
	case beforeType == afterType:
		return nil, []string{
			"the source and destination use the same type",
		}, true

	case afterType == collectionType("list", beforeType):
		return []TransformationKind{
				WrapInList,
			},
			[]string{
				"the original expression can be wrapped in a list",
			}, true
	case afterType == collectionType("set", beforeType):
		return []TransformationKind{
				WrapInSet,
			},
			[]string{
				"the original expression can be wrapped in a set",
			},
			true

	default:
		return nil, nil, false
	}

}

func flattenResource(resource schemadiff.ResourceSchema) map[string]schemaAttribute {
	result := make(map[string]schemaAttribute)

	flattenBlock(nil, schemadiff.BlockSchema{
		Attributes: resource.Attributes,
		BlockTypes: resource.BlockTypes,
	},
		result,
	)

	return result
}

func flattenBlock(blockPath []string, block schemadiff.BlockSchema, result map[string]schemaAttribute) {
	parent := strings.Join(blockPath, ".")

	attributeNames := make([]string, 0, len(block.Attributes))

	for name := range block.Attributes {
		attributeNames = append(attributeNames, name)
	}

	sort.Strings(attributeNames)

	for _, name := range attributeNames {
		attribute := block.Attributes[name]

		path := appendPath(blockPath, name)

		result[path] = schemaAttribute{
			Path:      path,
			Parent:    parent,
			Name:      name,
			Attribute: attribute,
		}
	}

	blockNames := make([]string, 0, len(block.BlockTypes))

	for name := range block.BlockTypes {
		blockNames = append(blockNames, name)
	}

	for _, name := range blockNames {
		nestedBlock := block.BlockTypes[name]

		nestedPath := make([]string, 0, len(blockPath)+1)

		nestedPath = append(nestedPath, blockPath...)

		nestedPath = append(nestedPath, name)

		flattenBlock(nestedPath, nestedBlock.Block, result)
	}
}

func findAddedAttributes(baseline map[string]schemaAttribute, upgraded map[string]schemaAttribute) []schemaAttribute {
	var added []schemaAttribute

	for path, attribute := range upgraded {
		if _, existed := baseline[path]; existed {
			continue
		}

		added = append(added, attribute)
	}

	sort.Slice(added, func(i, j int) bool {
		return added[i].Path < added[j].Path
	})

	return added
}

func sortCandidates(candidates []Candidate) {
	sort.Slice(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]

		leftSameType := left.BeforeType == left.AfterType

		rightSameType := right.BeforeType == right.AfterType

		if leftSameType != rightSameType {
			return leftSameType
		}

		leftRename := containsTransformation(left.Transformation, RenameAttribute)

		rightRename := containsTransformation(right.Transformation, RenameAttribute)

		if leftRename != rightRename {
			return leftRename
		}

		return left.ToPath < right.ToPath
	})
}

func containsTransformation(transformations []TransformationKind, expected TransformationKind) bool {
	for _, transformation := range transformations {
		if transformation == expected {
			return true
		}
	}

	return false
}

func isConfigurable(attribute schemadiff.AttributeSchema) bool {
	return attribute.Required || attribute.Optional
}

func appendPath(path []string, attribute string) string {
	parts := make([]string, 0, len(path)+1)

	parts = append(parts, path...)
	parts = append(parts, attribute)

	return strings.Join(parts, ".")
}

func collectionType(collection string, elementType string) string {
	if elementType == "" {
		return ""
	}

	return `["` + collection + `",` + elementType + `]`
}

func typeSignature(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var value any

	decoder := json.NewDecoder(
		bytes.NewReader(raw),
	)

	decoder.UseNumber()

	if err := decoder.Decode(&value); err != nil {
		return string(raw)
	}

	normalized, err := json.Marshal(value)

	if err != nil {
		return string(raw)
	}

	return string(normalized)
}
