package migration

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	version "github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

type RenameResourceAttributeRole struct {
	RuleID        string
	Provider      string
	IntroducedIn  string
	ResourceType  string
	FromAttribute string
	ToAttribute   string
	Description   string
}

func (rule RenameResourceAttributeRole) ID() string {
	return rule.RuleID
}

func (rule RenameResourceAttributeRole) Applies(context Context) (bool, error) {
	if rule.RuleID == "" {
		return false, fmt.Errorf("migration rule has no ID")
	}

	if rule.Provider != context.Provider {
		return false, nil
	}

	currentVersion, err := version.NewVersion(context.CurrentVersion)

	if err != nil {
		return false, fmt.Errorf("parse current version %q: %w", context.CurrentVersion, err)
	}

	targetVersion, err := version.NewVersion(context.TargetVersion)

	if err != nil {
		return false, fmt.Errorf("parse target version %q: %w", context.TargetVersion, err)
	}

	introducedVersion, err := version.NewVersion(rule.IntroducedIn)

	if err != nil {
		return false, fmt.Errorf("parse introduced version %q: %w", rule.IntroducedIn, err)
	}

	return currentVersion.LessThan(introducedVersion) && !targetVersion.LessThan(introducedVersion), nil
}

func (rule RenameResourceAttributeRole) Apply(context Context) ([]FileChange, error) {
	paths, err := filepath.Glob(filepath.Join(context.ProjectRoot, "*.tf"))

	if err != nil {
		return nil, fmt.Errorf("find terraform files: %w", err)
	}

	sort.Strings(paths)

	var changes []FileChange

	for _, path := range paths {
		change, err := rule.applyToFile(context.ProjectRoot, path)

		if err != nil {
			return nil, err
		}

		if change != nil {
			changes = append(changes, *change)
		}
	}

	return changes, nil
}

func (rule RenameResourceAttributeRole) applyToFile(projectRoot string, path string) (*FileChange, error) {
	original, err := os.ReadFile(path)

	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	file, diagnostics := hclwrite.ParseConfig(original, path, hcl.InitialPos)

	if diagnostics.HasErrors() {
		return nil, fmt.Errorf("parse %s: %s", path, diagnostics.Error())
	}

	modified := false

	for _, block := range file.Body().Blocks() {
		if block.Type() != "resource" {
			continue
		}

		labels := block.Labels()

		if len(labels) != 2 {
			continue
		}

		resourceType := labels[0]

		if resourceType != rule.ResourceType {
			continue
		}

		body := block.Body()

		oldAttribute := body.GetAttribute(rule.FromAttribute)

		if oldAttribute == nil {
			continue
		}

		if body.GetAttribute(rule.ToAttribute) != nil {
			return nil, fmt.Errorf("%s contains both %s and %s", path, rule.FromAttribute, rule.ToAttribute)
		}

		valueTokens := oldAttribute.Expr().BuildTokens(nil)

		body.RemoveAttribute(rule.FromAttribute)

		body.SetAttributeRaw(rule.ToAttribute, valueTokens)

		modified = true
	}

	if !modified {
		return nil, nil
	}

	updated := hclwrite.Format(file.Bytes())

	if bytes.Equal(original, updated) {
		return nil, nil
	}

	relativePath, err := filepath.Rel(projectRoot, path)

	if err != nil {
		return nil, fmt.Errorf("resolve relative path: %w", err)
	}

	info, err := os.Stat(path)

	if err != nil {
		return nil, fmt.Errorf("read file information: %w", err)
	}

	if err := os.WriteFile(path, updated, info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("write migrated file: %w", err)
	}

	return &FileChange{
		RelativePath: filepath.ToSlash(relativePath),
		RuleID:       rule.RuleID,
		Description:  rule.Description,
		Content:      append([]byte(nil), updated...),
	}, nil
}
