package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asierhr/infraupgrade/internal/migration"
)

type testMigrationRule struct {
	id      string
	changes []migration.FileChange
	err     error
}

func (rule testMigrationRule) ID() string { return rule.id }
func (rule testMigrationRule) Applies(migration.Context) (bool, error) {
	return true, nil
}
func (rule testMigrationRule) Apply(migration.Context) ([]migration.FileChange, error) {
	return rule.changes, rule.err
}

func TestApplyMigrationsConvertsAndSortsChanges(t *testing.T) {
	firstContent := []byte("first")
	engine := migration.Engine{Rules: []migration.Rule{testMigrationRule{
		id: "rule",
		changes: []migration.FileChange{
			{RelativePath: "z.tf", RuleID: "z-rule", Description: "z change", Content: []byte("z")},
			{RelativePath: "a.tf", RuleID: "a-rule", Description: "a change", Content: firstContent},
		},
	}}}

	changes, err := applyMigrations(engine, []ProviderTarget{{
		Name: "aws", CurrentVersion: "6.40.0", TargetVersion: "6.64.0",
	}}, t.TempDir())
	if err != nil {
		t.Fatalf("applyMigrations() error = %v", err)
	}
	if len(changes) != 2 || changes[0].RelativePath != "a.tf" || changes[1].RelativePath != "z.tf" {
		t.Fatalf("applyMigrations() = %#v", changes)
	}
	if changes[0].RuleID != "a-rule" || changes[0].Description != "a change" || string(changes[0].Content) != "first" {
		t.Fatalf("first applied migration = %#v", changes[0])
	}
	firstContent[0] = 'X'
	if string(changes[0].Content) != "first" {
		t.Fatal("applyMigrations() did not copy migration content")
	}
}

func TestApplyMigrationsRejectsTwoProvidersChangingSameFile(t *testing.T) {
	engine := migration.Engine{Rules: []migration.Rule{testMigrationRule{
		id:      "rule",
		changes: []migration.FileChange{{RelativePath: "main.tf", RuleID: "rule", Content: []byte("change")}},
	}}}
	_, err := applyMigrations(engine, []ProviderTarget{{Name: "aws"}, {Name: "random"}}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "modify main.tf") {
		t.Fatalf("applyMigrations() error = %v", err)
	}
}

func TestApplyMigrationsWrapsRuleError(t *testing.T) {
	engine := migration.Engine{Rules: []migration.Rule{testMigrationRule{id: "broken", err: errors.New("boom")}}}
	_, err := applyMigrations(engine, []ProviderTarget{{Name: "aws"}}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "apply aws migration") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("applyMigrations() error = %v", err)
	}
}

func TestDryRunWithMigrationsUsesTemporaryWorkspace(t *testing.T) {
	project := t.TempDir()
	original := `resource "aws_instance" "web" {
  old_ami = "ami-test"
}
`
	mustWriteFile(t, filepath.Join(project, "main.tf"), original)
	mustWriteFile(t, filepath.Join(project, ".terraform.lock.hcl"), baselineTestLock)

	baselineSchema := schemaWithAWSInstanceAttribute("old_ami")
	upgradedSchema := schemaWithAWSInstanceAttribute("new_ami")
	engine := migration.Engine{Rules: []migration.Rule{migration.RenameResourceAttributeRole{
		RuleID: "rename-ami", Provider: "aws", IntroducedIn: "6.50.0",
		ResourceType: "aws_instance", FromAttribute: "old_ami", ToAttribute: "new_ami",
		Description: "rename old_ami to new_ami",
	}}}

	report, err := DryRunWithMigrations(
		context.Background(), project,
		&fakePlanRunner{baselineSchema: baselineSchema, upgradedSchema: upgradedSchema},
		engine,
		[]ProviderTarget{{Name: "aws", CurrentVersion: "6.40.0", TargetVersion: "6.64.0"}},
	)
	if err != nil {
		t.Fatalf("DryRunWithMigrations() error = %v", err)
	}
	if len(report.AppliedMigrations) != 1 || report.AppliedMigrations[0].RelativePath != "main.tf" {
		t.Fatalf("applied migrations = %#v", report.AppliedMigrations)
	}
	content, err := os.ReadFile(filepath.Join(project, "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("original project was modified:\n%s", content)
	}
}

func schemaWithAWSInstanceAttribute(attribute string) string {
	return `{
  "format_version":"1.0",
  "provider_schemas":{
    "registry.terraform.io/hashicorp/aws":{
      "resource_schemas":{
        "aws_instance":{
          "block":{"attributes":{"` + attribute + `":{"type":"string","optional":true}}}
        }
      }
    }
  }
}`
}
