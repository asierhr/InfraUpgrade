package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenameResourceAttributeRuleAppliesAcrossIntroducedVersion(t *testing.T) {
	rule := RenameResourceAttributeRole{RuleID: "aws-example", Provider: "aws", IntroducedIn: "6.50.0"}
	tests := []struct {
		name    string
		context Context
		want    bool
		wantErr bool
	}{
		{name: "crosses version", context: Context{Provider: "aws", CurrentVersion: "6.40.0", TargetVersion: "6.64.0"}, want: true},
		{name: "target before", context: Context{Provider: "aws", CurrentVersion: "6.40.0", TargetVersion: "6.49.0"}},
		{name: "already introduced", context: Context{Provider: "aws", CurrentVersion: "6.50.0", TargetVersion: "6.64.0"}},
		{name: "different provider", context: Context{Provider: "azurerm", CurrentVersion: "bad", TargetVersion: "bad"}},
		{name: "invalid current", context: Context{Provider: "aws", CurrentVersion: "bad", TargetVersion: "6.64.0"}, wantErr: true},
		{name: "invalid target", context: Context{Provider: "aws", CurrentVersion: "6.40.0", TargetVersion: "bad"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := rule.Applies(test.context)
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("Applies() = %t, %v; want %t, error=%t", got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestRenameResourceAttributeRuleValidatesMetadata(t *testing.T) {
	_, err := (RenameResourceAttributeRole{}).Applies(Context{})
	if err == nil || !strings.Contains(err.Error(), "no ID") {
		t.Fatalf("Applies() error = %v", err)
	}

	rule := RenameResourceAttributeRole{RuleID: "rule", Provider: "aws", IntroducedIn: "invalid"}
	_, err = rule.Applies(Context{Provider: "aws", CurrentVersion: "1.0.0", TargetVersion: "2.0.0"})
	if err == nil || !strings.Contains(err.Error(), "introduced version") {
		t.Fatalf("Applies() error = %v", err)
	}
}

func TestRenameResourceAttributeRuleApply(t *testing.T) {
	root := t.TempDir()
	input := `resource "aws_example" "first" {
  old_name = var.value
}

resource "aws_other" "ignored" {
  old_name = "unchanged"
}
`
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	rule := RenameResourceAttributeRole{
		RuleID: "aws-example-rename", Provider: "aws", IntroducedIn: "6.50.0",
		ResourceType: "aws_example", FromAttribute: "old_name", ToAttribute: "new_name",
		Description: "rename old_name to new_name",
	}
	changes, err := rule.Apply(Context{Provider: "aws", CurrentVersion: "6.40.0", TargetVersion: "6.64.0", ProjectRoot: root})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changes) != 1 || changes[0].RelativePath != "main.tf" || changes[0].RuleID != rule.RuleID || changes[0].Description != rule.Description {
		t.Fatalf("Apply() = %#v", changes)
	}
	content := string(changes[0].Content)
	if !strings.Contains(content, "new_name = var.value") || strings.Contains(content, "old_name = var.value") {
		t.Fatalf("renamed content =\n%s", content)
	}
	if !strings.Contains(content, `old_name = "unchanged"`) {
		t.Fatalf("unrelated resource was changed:\n%s", content)
	}
}

func TestRenameResourceAttributeRuleApplyReturnsNoChanges(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte("resource \"aws_example\" \"x\" {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rule := RenameResourceAttributeRole{RuleID: "rule", ResourceType: "aws_example", FromAttribute: "old", ToAttribute: "new"}
	changes, err := rule.Apply(Context{ProjectRoot: root})
	if err != nil || len(changes) != 0 {
		t.Fatalf("Apply() = %#v, %v", changes, err)
	}
}
