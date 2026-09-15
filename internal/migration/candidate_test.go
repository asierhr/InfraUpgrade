package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asierhr/infraupgrade/internal/schemadiff"
)

func TestApplyCandidateRenamesAttributeAndPreservesExpression(t *testing.T) {
	project := t.TempDir()
	writeCandidateTestFile(t, project, "main.tf", `resource "example_resource" "example" {
  old_name = upper(var.name)
}
`)

	changes, err := ApplyCandidate(project, []Proposal{
		candidateTestProposal(nil, "old_name", "new_name", RenameAttribute),
	})
	if err != nil {
		t.Fatalf("ApplyCandidate() error = %v", err)
	}
	if len(changes) != 1 || changes[0].RelativePath != "main.tf" {
		t.Fatalf("ApplyCandidate() changes = %#v; want main.tf", changes)
	}

	content := readCandidateTestFile(t, project, "main.tf")
	if strings.Contains(content, "old_name") {
		t.Fatalf("migrated content still contains old_name:\n%s", content)
	}
	if !strings.Contains(content, "new_name = upper(var.name)") {
		t.Fatalf("migrated content did not preserve expression:\n%s", content)
	}
	if changes[0].RuleID != schemaDerivedRuleId || string(changes[0].Content) != content {
		t.Fatalf("ApplyCandidate() change = %#v", changes[0])
	}
}

func TestApplyCandidateMovesSeveralAttributesAndRemovesEmptySourceBlock(t *testing.T) {
	project := t.TempDir()
	writeCandidateTestFile(t, project, "main.tf", `resource "example_resource" "example" {
  legacy {
    first  = var.first
    second = var.second
  }
}
`)

	proposals := []Proposal{
		candidateTestProposal([]string{"legacy"}, "first", "settings.first", MoveAttribute),
		candidateTestProposal([]string{"legacy"}, "second", "settings.second", MoveAttribute),
	}

	_, err := ApplyCandidate(project, proposals)
	if err != nil {
		t.Fatalf("ApplyCandidate() error = %v", err)
	}

	content := readCandidateTestFile(t, project, "main.tf")
	if strings.Contains(content, "legacy {") {
		t.Fatalf("empty legacy block was not removed:\n%s", content)
	}
	for _, expected := range []string{
		"settings {",
		"first  = var.first",
		"second = var.second",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("migrated content does not contain %q:\n%s", expected, content)
		}
	}
}

func TestApplyCandidateWrapsExpressionsInCollections(t *testing.T) {
	tests := []struct {
		name           string
		transformation TransformationKind
		want           string
	}{
		{name: "list", transformation: WrapInList, want: `new_value = [var.value]`},
		{name: "set", transformation: WrapInSet, want: `new_value = toset([var.value])`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := t.TempDir()
			writeCandidateTestFile(t, project, "main.tf", `resource "example_resource" "example" {
  old_value = var.value
}
`)

			proposal := candidateTestProposal(
				nil,
				"old_value",
				"new_value",
				RenameAttribute,
				test.transformation,
			)

			if _, err := ApplyCandidate(project, []Proposal{proposal}); err != nil {
				t.Fatalf("ApplyCandidate() error = %v", err)
			}

			content := readCandidateTestFile(t, project, "main.tf")
			if !strings.Contains(content, test.want) {
				t.Fatalf("migrated content does not contain %q:\n%s", test.want, content)
			}
		})
	}
}

func TestApplyCandidateSkipsProposalsThatAreNotSelectedCandidates(t *testing.T) {
	project := t.TempDir()
	original := `resource "example_resource" "example" {
  old_name = "value"
}
`
	writeCandidateTestFile(t, project, "main.tf", original)

	unsupported := candidateTestProposal(nil, "old_name", "new_name", RenameAttribute)
	unsupported.Status = ProposalUnsupported

	changes, err := ApplyCandidate(project, []Proposal{unsupported})
	if err != nil {
		t.Fatalf("ApplyCandidate() error = %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("ApplyCandidate() changes = %#v; want none", changes)
	}
	if content := readCandidateTestFile(t, project, "main.tf"); content != original {
		t.Fatalf("content changed unexpectedly:\n%s", content)
	}
}

func TestApplyCandidateRejectsPathOutsideProject(t *testing.T) {
	project := t.TempDir()
	proposal := candidateTestProposal(nil, "old_name", "new_name", RenameAttribute)
	proposal.Assignment.File = "../outside.tf"

	_, err := ApplyCandidate(project, []Proposal{proposal})
	if err == nil || !strings.Contains(err.Error(), "leaves project root") {
		t.Fatalf("ApplyCandidate() error = %v; want path traversal error", err)
	}
}

func TestApplyCandidateRejectsExistingDestination(t *testing.T) {
	project := t.TempDir()
	writeCandidateTestFile(t, project, "main.tf", `resource "example_resource" "example" {
  old_name = "old"
  new_name = "new"
}
`)

	_, err := ApplyCandidate(project, []Proposal{
		candidateTestProposal(nil, "old_name", "new_name", RenameAttribute),
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("ApplyCandidate() error = %v; want destination conflict", err)
	}
}

func candidateTestProposal(
	blockPath []string,
	from string,
	to string,
	transformations ...TransformationKind,
) Proposal {
	candidate := Candidate{
		ToPath:         to,
		Transformation: transformations,
	}

	return Proposal{
		Assignment: schemadiff.Assignment{
			File:         "main.tf",
			ResourceType: "example_resource",
			ResourceName: "example",
			BlockPath:    blockPath,
			Attribute:    from,
		},
		Status:     ProposalCandidate,
		Candidates: []Candidate{candidate},
		Selected:   &candidate,
	}
}

func writeCandidateTestFile(t *testing.T, root string, relativePath string, content string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create test directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}

func readCandidateTestFile(t *testing.T, root string, relativePath string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relativePath)))
	if err != nil {
		t.Fatalf("read test file: %v", err)
	}

	return string(content)
}
