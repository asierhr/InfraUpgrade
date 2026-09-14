package schemadiff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

type ChangeKind string

const (
	ResourceRemoved      ChangeKind = "resource-removed"
	NestedBlockRemoved   ChangeKind = "nested-block-removed"
	AttributeRemoved     ChangeKind = "attribute-removed"
	AttributeTypeChanged ChangeKind = "attribute-type-changed"
	ConfigurabilityLost  ChangeKind = "configurability-lost"
	RequirementChanged   ChangeKind = "required-changed"
	NestingModeChanged   ChangeKind = "nesting-mode-changed"
)

type AttributeSchema struct {
	Type        json.RawMessage `json:"type"`
	Description string          `json:"description"`
	Required    bool            `json:"required"`
	Optional    bool            `json:"optional"`
	Computed    bool            `json:"computed"`
	Sensitive   bool            `json:"sensitive"`
}

type ResourceSchema struct {
	Provider   string
	Attributes map[string]AttributeSchema
	BlockTypes map[string]NestedBlockSchema
}

type Snapshot struct {
	Resources map[string]ResourceSchema
}

type Assignment struct {
	File         string
	ResourceType string
	ResourceName string
	BlockPath    []string
	Attribute    string
	Expression   string
}

type AssigmentChange struct {
	Assignment Assignment
	Kind       ChangeKind
	Before     *AttributeSchema
	After      *AttributeSchema
	BeforeType string
	AfterType  string

	BeforeNestingMode string
	AfterNestingMode  string
}

type Report struct {
	Assigments []Assignment
	Changes    []AssigmentChange
}

type resolvedAssignment struct {
	Attribute    AttributeSchema
	Found        bool
	BlockExists  bool
	NestingModes []string
}

type BlockSchema struct {
	Attributes map[string]AttributeSchema   `json:"attributes"`
	BlockTypes map[string]NestedBlockSchema `json:"block_types"`
}

type NestedBlockSchema struct {
	NestingMode string      `json:"nesting_mode"`
	MinItems    int         `json:"min_items"`
	MaxItems    int         `json:"max_items"`
	Block       BlockSchema `json:"block"`
}

type schemaDocument struct {
	FormatVersion  string                    `json:"format_version"`
	ProviderSchema map[string]providerSchema `json:"provider_schemas"`
}

type providerSchema struct {
	ResourceSchemas map[string]schemaRepresentation `json:"resource_schemas"`
}

type schemaRepresentation struct {
	Block BlockSchema `json:"block"`
}

func (assignment Assignment) FullPath() string {
	parts := make([]string, 0, len(assignment.BlockPath)+1)

	parts = append(parts, assignment.BlockPath...)
	parts = append(parts, assignment.Attribute)

	return strings.Join(parts, ".")
}

func Detect(projectRoot string, baselineSchemaJSON []byte, upgradedSchemaJSON []byte) (Report, error) {
	baseline, err := ParseSnapshot(baselineSchemaJSON)

	if err != nil {
		return Report{}, fmt.Errorf("parse baseline schema: %w", err)
	}

	upgraded, err := ParseSnapshot(upgradedSchemaJSON)

	if err != nil {
		return Report{}, fmt.Errorf("parse upgraded schema: %w", err)
	}

	assigments, err := ScanAssigments(projectRoot)

	if err != nil {
		return Report{}, err
	}

	changes, err := CompareAssignments(assigments, baseline, upgraded)

	if err != nil {
		return Report{}, err
	}

	return Report{
		Assigments: assigments,
		Changes:    changes,
	}, nil
}

func ParseSnapshot(content []byte) (Snapshot, error) {
	var document schemaDocument

	if err := json.Unmarshal(content, &document); err != nil {
		return Snapshot{}, fmt.Errorf("decode provider schema JSON: %w", err)
	}

	if document.FormatVersion == "" {
		return Snapshot{}, fmt.Errorf("provider schema has no format version")
	}

	majorVersion := strings.SplitN(document.FormatVersion, ".", 2)[0]

	if majorVersion != "1" {
		return Snapshot{}, fmt.Errorf("unsupported provider schema format: %q", document.FormatVersion)
	}

	snapshot := Snapshot{
		Resources: make(map[string]ResourceSchema),
	}

	for provider, providerSchema := range document.ProviderSchema {
		for resourceType, resource := range providerSchema.ResourceSchemas {
			if previous, exists := snapshot.Resources[resourceType]; exists {
				return Snapshot{}, fmt.Errorf("resource %s is exposed by providers %s and %s", resourceType, previous.Provider, provider)
			}

			attributes := make(map[string]AttributeSchema)

			for name, attribute := range resource.Block.Attributes {
				attributes[name] = attribute
			}

			snapshot.Resources[resourceType] = ResourceSchema{
				Provider:   provider,
				Attributes: attributes,
				BlockTypes: resource.Block.BlockTypes,
			}
		}
	}

	return snapshot, nil
}

func ScanAssigments(projectRoot string) ([]Assignment, error) {
	absoluteRoot, err := filepath.Abs(projectRoot)

	if err != nil {
		return nil, fmt.Errorf("resolve project path: %w", err)
	}

	paths, err := filepath.Glob(filepath.Join(absoluteRoot, "*.tf"))

	if err != nil {
		return nil, fmt.Errorf("find Terraform files: %w", err)
	}

	sort.Strings(paths)

	var assigments []Assignment

	for _, path := range paths {
		fileAssigments, err := scanFileAssigments(absoluteRoot, path)

		if err != nil {
			return nil, err
		}

		assigments = append(assigments, fileAssigments...)
	}

	sort.Slice(assigments, func(i, j int) bool {
		left := assigments[i]
		right := assigments[j]

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

	return assigments, nil
}

func scanFileAssigments(projectRoot string, path string) ([]Assignment, error) {
	content, err := os.ReadFile(path)

	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	file, diagnostics := hclwrite.ParseConfig(
		content,
		path,
		hcl.InitialPos,
	)

	if diagnostics.HasErrors() {
		return nil, fmt.Errorf("parse %s: %s", path, diagnostics.Error())
	}

	relativePath, err := filepath.Rel(projectRoot, path)

	if err != nil {
		return nil, fmt.Errorf("resolve relative path for %s: %w", path, err)
	}

	var assignments []Assignment

	for _, block := range file.Body().Blocks() {
		if block.Type() != "resource" {
			continue
		}

		labels := block.Labels()

		if len(labels) != 2 {
			continue
		}

		assignments = append(
			assignments,
			scanBodyAssignments(
				filepath.ToSlash(relativePath),
				labels[0],
				labels[1],
				nil,
				block.Body(),
			)...,
		)
	}

	return assignments, nil
}

func scanBodyAssignments(file string, resourceType string, resourceName string, blockPath []string, body *hclwrite.Body) []Assignment {
	var assignments []Assignment

	for name, attribute := range body.Attributes() {
		expressionTokens := attribute.Expr().BuildTokens(nil)

		assignments = append(assignments, Assignment{
			File:         file,
			ResourceType: resourceType,
			ResourceName: resourceName,
			BlockPath: append(
				[]string(nil),
				blockPath...,
			),
			Attribute: name,
			Expression: strings.TrimSpace(
				string(expressionTokens.Bytes()),
			),
		})
	}

	for _, block := range body.Blocks() {
		if block.Type() == "dynamic" {
			assignments = append(assignments, scanDynamicBlockAssignments(
				file,
				resourceType,
				resourceName,
				blockPath,
				block,
			)...,
			)

			continue
		}

		nestedPath := appendPath(blockPath, block.Type())

		assignments = append(assignments, scanBodyAssignments(
			file,
			resourceType,
			resourceName,
			nestedPath,
			block.Body(),
		)...,
		)
	}
	return assignments
}

func scanDynamicBlockAssignments(file string, resourceType string, resourceName string, blockPath []string, dynamicBlock *hclwrite.Block) []Assignment {
	labels := dynamicBlock.Labels()

	if len(labels) != 1 {
		return nil
	}

	dynamicBlockName := labels[0]

	for _, child := range dynamicBlock.Body().Blocks() {
		if child.Type() != "content" {
			continue
		}

		return scanBodyAssignments(
			file,
			resourceType,
			resourceName,
			appendPath(blockPath, dynamicBlockName),
			child.Body(),
		)
	}

	return nil
}

func appendPath(path []string, element string) []string {
	result := make([]string, 0, len(path)+1)
	result = append(result, path...)
	result = append(result, element)

	return result
}

func CompareAssignments(assignments []Assignment, baseline Snapshot, upgraded Snapshot) ([]AssigmentChange, error) {
	var changes []AssigmentChange

	for _, assignment := range assignments {
		baselineResource, baselineExists := baseline.Resources[assignment.ResourceType]

		if !baselineExists {
			continue
		}

		upgradedResource, upgradedExists := upgraded.Resources[assignment.ResourceType]

		if !upgradedExists {
			changes = append(changes, AssigmentChange{
				Assignment: assignment,
				Kind:       ResourceRemoved,
			})

			continue
		}

		beforeResult := resolveAssigment(baselineResource, assignment)

		if !beforeResult.BlockExists || !beforeResult.Found {
			continue
		}

		afterResult := resolveAssigment(upgradedResource, assignment)

		before := beforeResult.Attribute
		beforeCopy := before
		beforeType := typeSignature(before.Type)

		if !afterResult.BlockExists {
			changes = append(changes, AssigmentChange{
				Assignment: assignment,
				Kind:       NestedBlockRemoved,
				Before:     &beforeCopy,
				BeforeType: beforeType,
			},
			)

			continue
		}

		if !afterResult.Found {

			changes = append(changes, AssigmentChange{
				Assignment: assignment,
				Kind:       AttributeRemoved,
				Before:     &beforeCopy,
				BeforeType: beforeType,
			})

			continue
		}

		after := afterResult.Attribute
		afterCopy := after
		afterType := typeSignature(after.Type)

		beforeNesting := nestingModeSignature(
			beforeResult.NestingModes,
		)

		afterNesting := nestingModeSignature(
			afterResult.NestingModes,
		)

		if beforeNesting != afterNesting {
			changes = append(changes, AssigmentChange{
				Assignment:        assignment,
				Kind:              NestingModeChanged,
				Before:            &beforeCopy,
				After:             &afterCopy,
				BeforeType:        beforeType,
				AfterType:         afterType,
				BeforeNestingMode: beforeNesting,
				AfterNestingMode:  afterNesting,
			})
		}

		if beforeType != afterType {
			changes = append(changes, AssigmentChange{
				Assignment: assignment,
				Kind:       AttributeTypeChanged,
				Before:     &beforeCopy,
				After:      &afterCopy,
				BeforeType: beforeType,
				AfterType:  afterType,
			},
			)
		}

		if isConfigurable(before) && !isConfigurable(after) {
			changes = append(changes, AssigmentChange{
				Assignment: assignment,
				Kind:       ConfigurabilityLost,
				Before:     &beforeCopy,
				After:      &afterCopy,
				BeforeType: beforeType,
				AfterType:  afterType,
			})
		}

		if before.Required != after.Required || before.Optional != after.Optional {
			changes = append(changes, AssigmentChange{
				Assignment: assignment,
				Kind:       RequirementChanged,
				Before:     &beforeCopy,
				After:      &afterCopy,
				BeforeType: beforeType,
				AfterType:  afterType,
			})
		}
	}

	sort.Slice(changes, func(i, j int) bool {
		left := changes[i]
		right := changes[j]

		if left.Assignment.File != right.Assignment.File {
			return left.Assignment.File < right.Assignment.File
		}

		if left.Assignment.ResourceType != right.Assignment.ResourceType {
			return left.Assignment.ResourceType < right.Assignment.ResourceType
		}

		if left.Assignment.ResourceName != right.Assignment.ResourceName {
			return left.Assignment.ResourceName < right.Assignment.ResourceName
		}

		if left.Assignment.FullPath() != right.Assignment.FullPath() {
			return left.Assignment.FullPath() < right.Assignment.FullPath()
		}

		return left.Kind < right.Kind
	})

	return changes, nil
}

func resolveAssigment(resource ResourceSchema, assignment Assignment) resolvedAssignment {

	block := BlockSchema{
		Attributes: resource.Attributes,
		BlockTypes: resource.BlockTypes,
	}

	nestingModes := make([]string, 0, len(assignment.BlockPath))

	for _, blockName := range assignment.BlockPath {
		nestedBlock, exists := block.BlockTypes[blockName]

		if !exists {
			return resolvedAssignment{
				BlockExists:  false,
				NestingModes: nestingModes,
			}
		}

		nestingModes = append(nestingModes, nestedBlock.NestingMode)

		block = nestedBlock.Block
	}

	attribute, exists := block.Attributes[assignment.Attribute]

	return resolvedAssignment{
		Attribute:    attribute,
		Found:        exists,
		BlockExists:  true,
		NestingModes: nestingModes,
	}
}

func isConfigurable(attribute AttributeSchema) bool {
	return attribute.Required || attribute.Optional
}

func typeSignature(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var value any

	decoder := json.NewDecoder(bytes.NewReader(raw))
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

func nestingModeSignature(modes []string) string {
	return strings.Join(modes, ".")
}
