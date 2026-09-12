package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildProviderDeclarationChangeCreatesVersionsFile(t *testing.T) {
	change, err := BuildProviderDeclarationChange(t.TempDir(), []ProviderDeclaration{
		{Name: "random", Source: "hashicorp/random", Constraint: "~> 3.0"},
		{Name: "aws", Source: "hashicorp/aws", Constraint: "~> 6.64.0"},
	})
	if err != nil {
		t.Fatalf("BuildProviderDeclarationChange() error = %v", err)
	}
	if change == nil || change.RelativePath != "versions.tf" || change.Kind != ChangeProviderDeclaration {
		t.Fatalf("BuildProviderDeclarationChange() = %#v", change)
	}

	content := string(change.Content)
	for _, expected := range []string{`required_providers`, `aws = {`, `source  = "hashicorp/aws"`, `version = "~> 6.64.0"`, `random = {`} {
		if !strings.Contains(content, expected) {
			t.Errorf("generated content does not contain %q:\n%s", expected, content)
		}
	}
	if strings.Index(content, "aws = {") > strings.Index(content, "random = {") {
		t.Errorf("providers are not sorted:\n%s", content)
	}
}

func TestBuildProviderDeclarationChangePreservesExistingContent(t *testing.T) {
	root := t.TempDir()
	original := []byte("terraform {\n  required_version = \">= 1.7.0\"\n}\n")
	if err := os.WriteFile(filepath.Join(root, "versions.tf"), original, 0o600); err != nil {
		t.Fatal(err)
	}

	change, err := BuildProviderDeclarationChange(root, []ProviderDeclaration{{
		Name: "aws", Source: "hashicorp/aws", Constraint: "~> 6.64.0",
	}})
	if err != nil {
		t.Fatalf("BuildProviderDeclarationChange() error = %v", err)
	}
	content := string(change.Content)
	if !strings.Contains(content, `required_version = ">= 1.7.0"`) || !strings.Contains(content, "required_providers") {
		t.Fatalf("generated content did not preserve existing block:\n%s", content)
	}
}

func TestBuildProviderDeclarationChangeReturnsNilWithoutDeclarations(t *testing.T) {
	change, err := BuildProviderDeclarationChange(t.TempDir(), nil)
	if err != nil || change != nil {
		t.Fatalf("BuildProviderDeclarationChange(nil) = %#v, %v", change, err)
	}
}

func TestBuildProviderDeclarationChangeRejectsExistingDeclaration(t *testing.T) {
	root := t.TempDir()
	content := []byte("terraform {\n  required_providers = {\n    aws = { source = \"hashicorp/aws\" }\n  }\n}\n")
	if err := os.WriteFile(filepath.Join(root, "providers.tf"), content, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := BuildProviderDeclarationChange(root, []ProviderDeclaration{{Name: "aws", Source: "hashicorp/aws", Constraint: "~> 6.0"}})
	if err == nil || !strings.Contains(err.Error(), "existing required_providers") {
		t.Fatalf("BuildProviderDeclarationChange() error = %v", err)
	}
}

func TestBuildProviderDeclarationChangeValidatesDeclarations(t *testing.T) {
	tests := []struct {
		name        string
		declaration ProviderDeclaration
		want        string
	}{
		{name: "invalid name", declaration: ProviderDeclaration{Name: "not valid", Source: "hashicorp/aws", Constraint: "~> 6.0"}, want: "invalid provider name"},
		{name: "missing source", declaration: ProviderDeclaration{Name: "aws", Constraint: "~> 6.0"}, want: "has no source"},
		{name: "missing constraint", declaration: ProviderDeclaration{Name: "aws", Source: "hashicorp/aws"}, want: "has no version constraint"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildProviderDeclarationChange(t.TempDir(), []ProviderDeclaration{test.declaration})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BuildProviderDeclarationChange() error = %v; want containing %q", err, test.want)
			}
		})
	}
}

func TestBuildProviderDeclarationChangeRejectsInvalidTerraform(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte("terraform {"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := BuildProviderDeclarationChange(root, []ProviderDeclaration{{Name: "aws", Source: "hashicorp/aws", Constraint: "~> 6.0"}})
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("BuildProviderDeclarationChange() error = %v; want parse error", err)
	}
}
