package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

const schemaDerivedRuleId = "schema-derived"

func ApplyCandidate(projectRoot string, proposals []Proposal) ([]FileChange, error) {
	proposalsByFile := make(map[string][]Proposal)

	for _, proposal := range proposals {
		if proposal.Status != ProposalCandidate || proposal.Selected == nil {
			continue
		}

		proposalsByFile[proposal.Assignment.File] = append(proposalsByFile[proposal.Assignment.File], proposal)
	}

	files := make([]string, 0, len(proposalsByFile))

	for file := range proposalsByFile {
		files = append(files, file)
	}

	changes := make([]FileChange, 0, len(files))

	for _, relativePath := range files {
		change, err := applyCandidatesToFile(projectRoot, relativePath, proposalsByFile[relativePath])

		if err != nil {
			return nil, err
		}

		if change != nil {
			changes = append(changes, *change)
		}
	}

	return changes, nil
}

func applyCandidatesToFile(projectRoot string, relativePath string, proposals []Proposal) (*FileChange, error) {
	path, err := safeProjectPath(projectRoot, relativePath)

	if err != nil {
		return nil, err
	}

	original, err := os.ReadFile(path)

	if err != nil {
		return nil, fmt.Errorf("read migration file %s: %w", path, err)
	}

	file, diagnostics := hclwrite.ParseConfig(
		original,
		path,
		hcl.InitialPos,
	)

	if diagnostics.HasErrors() {
		return nil, fmt.Errorf("parse migration file %s: %s", path, diagnostics.Error())
	}

	sort.Slice(proposals, func(i, j int) bool {
		left := proposals[i].Assignment
		right := proposals[j].Assignment

		if left.ResourceType != right.ResourceType {
			return left.ResourceType < right.ResourceType
		}

		if left.ResourceName != right.ResourceName {
			return left.ResourceName < right.ResourceName
		}

		return left.FullPath() < right.FullPath()
	})

	for _, proposal := range proposals {
		if err := applyProposal(file, proposal); err != nil {
			return nil, fmt.Errorf("apply proposal for %s.%s.%s: %w", proposal.Assignment.ResourceType, proposal.Assignment.ResourceName, proposal.Assignment.FullPath(), err)
		}
	}

	pruneEmptyBlocks(file.Body())

	updated := hclwrite.Format(file.Bytes())

	info, err := os.Stat(path)

	if err != nil {
		return nil, fmt.Errorf("read migration file information: %w", err)
	}

	if err := os.WriteFile(path, updated, info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("write migrated file %s: %w", path, err)
	}

	return &FileChange{
		RelativePath: filepath.ToSlash(relativePath),
		RuleID:       schemaDerivedRuleId,
		Description:  "migration inferred from provider schema changes",
		Content:      append([]byte(nil), updated...),
	}, nil
}

func applyProposal(file *hclwrite.File, proposal Proposal) error {
	if proposal.Selected == nil {
		return fmt.Errorf("proposal has no selected candidate")
	}

	resourceBody, err := findResourceBody(file, proposal.Assignment.ResourceType, proposal.Assignment.ResourceName)

	if err != nil {
		return err
	}

	sourceBody, err := findExistingBody(resourceBody, proposal.Assignment.BlockPath)

	if err != nil {
		return fmt.Errorf("find source block: %w", err)
	}

	sourceAttribute := sourceBody.GetAttribute(proposal.Assignment.Attribute)

	if sourceAttribute == nil {
		return fmt.Errorf("source attribute %q was not found", proposal.Assignment.FullPath())
	}

	destinationParts := strings.Split(proposal.Selected.ToPath, ".")

	if len(destinationParts) == 0 {
		return fmt.Errorf("destination path is empty")
	}

	destinationAttribute := destinationParts[len(destinationParts)-1]
	destinationBlockPath := destinationParts[:len(destinationParts)-1]

	destinationBody, err := findOrCreateBody(resourceBody, destinationBlockPath)

	if err != nil {
		return fmt.Errorf("find destination block: %w", err)
	}

	existingDestination := destinationBody.GetAttribute(destinationAttribute)

	if existingDestination != nil {
		return fmt.Errorf("destination attribute %q already exists", proposal.Selected.ToPath)
	}

	expression := sourceAttribute.Expr().BuildTokens(nil)

	expression, err = transformExpression(expression, proposal.Selected.Transformation)

	if err != nil {
		return err
	}

	destinationBody.SetAttributeRaw(destinationAttribute, expression)

	sourceBody.RemoveAttribute(proposal.Assignment.Attribute)

	return nil
}

func transformExpression(expression hclwrite.Tokens, transformations []TransformationKind) (hclwrite.Tokens, error) {
	result := expression

	for _, transformation := range transformations {
		switch transformation {
		case RenameAttribute, MoveAttribute:
			// Estas transformaciones afectan a la ubicación, no al valor.

		case WrapInList:
			result = hclwrite.TokensForTuple([]hclwrite.Tokens{result})

		case WrapInSet:
			listExpression := hclwrite.TokensForTuple([]hclwrite.Tokens{result})

			result = hclwrite.TokensForFunctionCall("toset", listExpression)

		default:
			return nil, fmt.Errorf("unsupported transformation %q", transformation)

		}
	}
	return result, nil
}

func findResourceBody(file *hclwrite.File, resourceType string, resourceName string) (*hclwrite.Body, error) {
	var result *hclwrite.Body

	for _, block := range file.Body().Blocks() {
		if block.Type() != "resource" {
			continue
		}

		labels := block.Labels()

		if len(labels) != 2 {
			continue
		}

		if labels[0] != resourceType || labels[1] != resourceName {
			continue
		}

		if result != nil {
			return nil, fmt.Errorf("resource %s.%s is declared more than once in the same file", resourceType, resourceName)
		}

		result = block.Body()
	}

	if result == nil {
		return nil, fmt.Errorf("resource %s.%s was not found", resourceType, resourceName)
	}

	return result, nil
}

func findExistingBody(root *hclwrite.Body, blockPath []string) (*hclwrite.Body, error) {
	current := root

	for _, blockName := range blockPath {
		matches := blocksByType(current, blockName)

		if len(matches) == 0 {
			return nil, fmt.Errorf("block %q was not found", blockName)
		}

		if len(matches) > 1 {
			return nil, fmt.Errorf("block %q occurs more than once; migration is ambiguous", blockName)
		}

		current = matches[0].Body()
	}

	return current, nil
}

func findOrCreateBody(root *hclwrite.Body, blockPath []string) (*hclwrite.Body, error) {
	current := root

	for _, blockName := range blockPath {
		matches := blocksByType(current, blockName)

		if len(matches) > 1 {
			return nil, fmt.Errorf("destination block %q occurs more than once", blockName)
		}

		if len(matches) == 1 {
			current = matches[0].Body()
			continue
		}

		block := hclwrite.NewBlock(blockName, nil)

		current.AppendNewline()
		current.AppendBlock(block)

		current = block.Body()
	}

	return current, nil
}

func blocksByType(body *hclwrite.Body, blockType string) []*hclwrite.Block {
	var matches []*hclwrite.Block

	for _, block := range body.Blocks() {
		if block.Type() == blockType {
			matches = append(matches, block)
		}
	}

	return matches
}

func pruneEmptyBlocks(body *hclwrite.Body) {
	for _, block := range body.Blocks() {
		pruneEmptyBlocks(block.Body())

		if block.Type() == "resource" {
			continue
		}

		if len(block.Body().Attributes()) == 0 && len(block.Body().Blocks()) == 0 {
			body.RemoveBlock(block)
		}
	}
}

func safeProjectPath(projectRoot string, relativePath string) (string, error) {
	root, err := filepath.Abs(projectRoot)

	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}

	path, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(relativePath)))

	if err != nil {
		return "", fmt.Errorf("resolve migration path: %w", err)
	}

	relative, err := filepath.Rel(root, path)

	if err != nil {
		return "", fmt.Errorf("validate migration path: %w", err)
	}

	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("migration path leaves project root: %s", relativePath)
	}

	return path, nil
}
