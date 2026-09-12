package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

type ProviderDeclaration struct {
	Name       string
	Source     string
	Constraint string
}

func BuildProviderDeclarationChange(projectRoot string, declarations []ProviderDeclaration) (*FileChange, error) {
	if len(declarations) == 0 {
		return nil, nil
	}

	absoluteRoot, err := filepath.Abs(projectRoot)

	if err != nil {
		return nil, fmt.Errorf("resolve project path: %w", err)
	}

	hasRequiresProviders, err := projectHasRequiredProviders(absoluteRoot)

	if err != nil {
		return nil, err
	}

	if hasRequiresProviders {
		return nil, fmt.Errorf("editing an existing required_providers attribute is not supported yet")
	}

	sortedDeclarations := append([]ProviderDeclaration(nil), declarations...)

	sort.Slice(sortedDeclarations, func(i, j int) bool {
		return sortedDeclarations[i].Name < sortedDeclarations[j].Name
	})

	for _, declaration := range sortedDeclarations {
		if !hclsyntax.ValidIdentifier(declaration.Name) {
			return nil, fmt.Errorf("invalid provider name: %s", declaration.Name)
		}

		if declaration.Source == "" {
			return nil, fmt.Errorf("provider %s has no source", declaration.Name)
		}

		if declaration.Constraint == "" {
			return nil, fmt.Errorf("provider %s has no version constraint", declaration.Name)
		}
	}

	targetPath := filepath.Join(absoluteRoot, "versions.tf")

	original, err := readOptionalFile(targetPath)

	if err != nil {
		return nil, fmt.Errorf("read versions.tf: %w", err)
	}

	var file *hclwrite.File

	if original == nil {
		file = hclwrite.NewEmptyFile()
	} else {

		var diagnostics hcl.Diagnostics

		file, diagnostics = hclwrite.ParseConfig(
			original,
			targetPath,
			hcl.InitialPos,
		)

		if diagnostics.HasErrors() {
			return nil, fmt.Errorf("parse versions.tf: %s", diagnostics.Error())
		}

		file.Body().AppendNewline()
	}

	terraformBlock := hclwrite.NewBlock("terraform", nil)

	terraformBlock.Body().SetAttributeRaw(
		"required_providers",
		buildRequieredProviderTokens(sortedDeclarations),
	)

	file.Body().AppendBlock(terraformBlock)
	file.Body().AppendNewline()

	content := hclwrite.Format(file.Bytes())

	return &FileChange{
		RelativePath: "versions.tf",
		Kind:         ChangeProviderDeclaration,
		Content: append(
			[]byte(nil),
			content...,
		),
	}, nil
}

func buildRequieredProviderTokens(declarations []ProviderDeclaration) hclwrite.Tokens {
	providers := make([]hclwrite.ObjectAttrTokens, 0, len(declarations))

	for _, declaration := range declarations {
		settings := []hclwrite.ObjectAttrTokens{
			{
				Name: hclwrite.TokensForIdentifier(
					"source",
				),
				Value: hclwrite.TokensForValue(
					cty.StringVal(
						declaration.Source,
					),
				),
			},
			{
				Name: hclwrite.TokensForIdentifier(
					"version",
				),
				Value: hclwrite.TokensForValue(
					cty.StringVal(
						declaration.Constraint,
					),
				),
			},
		}

		providers = append(providers, hclwrite.ObjectAttrTokens{
			Name: hclwrite.TokensForIdentifier(
				declaration.Name,
			),
			Value: hclwrite.TokensForObject(settings),
		})
	}

	return hclwrite.TokensForObject(providers)
}

func projectHasRequiredProviders(projectRoot string) (bool, error) {
	paths, err := filepath.Glob(filepath.Join(projectRoot, "*.tf"))

	if err != nil {
		return false, fmt.Errorf("find terraform files: %w", err)
	}

	sort.Strings(paths)

	for _, path := range paths {
		content, err := os.ReadFile(path)

		if err != nil {
			return false, fmt.Errorf("read %s: %w", path, err)
		}

		file, diagnostics := hclwrite.ParseConfig(content, path, hcl.InitialPos)

		if diagnostics.HasErrors() {
			return false, fmt.Errorf("parse %s: %s", path, diagnostics.Error())
		}

		for _, block := range file.Body().Blocks() {
			if block.Type() != "terraform" {
				continue
			}

			if block.Body().GetAttribute("required_providers") != nil {
				return true, nil
			}
		}
	}
	return false, nil
}
