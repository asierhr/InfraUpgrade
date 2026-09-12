package upgrade

import (
	"strings"
	"testing"
)

func TestBuildLockfileChangeSet(t *testing.T) {
	content := []byte("provider lock content")
	report := successfulReportForChangeSet()
	report.Upgraded.LockFileChanged = true
	report.Upgraded.LockFileContent = content

	changeSet, err := BuildLockfileChangeSet(report)
	if err != nil {
		t.Fatalf("BuildLockfileChangeSet() error = %v", err)
	}
	if len(changeSet.Files) != 1 {
		t.Fatalf("BuildLockfileChangeSet() files = %d; want 1", len(changeSet.Files))
	}
	change := changeSet.Files[0]
	if change.RelativePath != ".terraform.lock.hcl" || change.Kind != ChangeLockFile || string(change.Content) != string(content) {
		t.Fatalf("BuildLockfileChangeSet() = %#v", change)
	}

	content[0] = 'X'
	if string(change.Content) != "provider lock content" {
		t.Fatal("BuildLockfileChangeSet() did not copy lockfile content")
	}
}

func TestBuildLockfileChangeSetRejectsInvalidReport(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Report)
		want   string
	}{
		{name: "failed upgrade", mutate: func(report *Report) { report.Upgraded.PlanAvailable = false }, want: "failed upgrade"},
		{name: "unchanged lockfile", mutate: func(report *Report) { report.Upgraded.LockFileChanged = false }, want: "did not change"},
		{name: "empty lockfile", mutate: func(report *Report) { report.Upgraded.LockFileContent = nil }, want: "is empty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := successfulReportForChangeSet()
			report.Upgraded.LockFileChanged = true
			report.Upgraded.LockFileContent = []byte("lock")
			test.mutate(&report)

			_, err := BuildLockfileChangeSet(report)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BuildLockfileChangeSet() error = %v; want containing %q", err, test.want)
			}
		})
	}
}

func TestBuildPrepareChangeSetAddsProviderDeclaration(t *testing.T) {
	report := successfulReportForChangeSet()
	report.Upgraded.LockFileChanged = true
	report.Upgraded.LockFileContent = []byte("lock")

	changeSet, err := BuildPrepareChangeSet(report, t.TempDir(), []ProviderDeclaration{{
		Name: "aws", Source: "hashicorp/aws", Constraint: "~> 6.64.0",
	}})
	if err != nil {
		t.Fatalf("BuildPrepareChangeSet() error = %v", err)
	}
	if len(changeSet.Files) != 2 {
		t.Fatalf("BuildPrepareChangeSet() files = %#v; want 2", changeSet.Files)
	}
	if changeSet.Files[1].RelativePath != "versions.tf" || changeSet.Files[1].Kind != ChangeProviderDeclaration {
		t.Fatalf("provider declaration change = %#v", changeSet.Files[1])
	}
}

func TestValidateChangeSet(t *testing.T) {
	validLock := FileChange{RelativePath: ".terraform.lock.hcl", Kind: ChangeLockFile, Content: []byte("lock")}
	tests := []struct {
		name      string
		changeSet ChangeSet
		wantError string
	}{
		{name: "valid", changeSet: ChangeSet{Files: []FileChange{validLock}}},
		{name: "empty", changeSet: ChangeSet{}, wantError: "no files"},
		{name: "absolute", changeSet: ChangeSet{Files: []FileChange{{RelativePath: `C:\\outside.tf`, Kind: ChangeProviderDeclaration, Content: []byte("x")}}}, wantError: "invalid changeset path"},
		{name: "parent traversal", changeSet: ChangeSet{Files: []FileChange{{RelativePath: "../outside.tf", Kind: ChangeProviderDeclaration, Content: []byte("x")}}}, wantError: "invalid changeset path"},
		{name: "duplicate", changeSet: ChangeSet{Files: []FileChange{validLock, validLock}}, wantError: "duplicate"},
		{name: "wrong lock path", changeSet: ChangeSet{Files: []FileChange{{RelativePath: "lock.hcl", Kind: ChangeLockFile, Content: []byte("x")}}}, wantError: "unexpected path"},
		{name: "declaration is not tf", changeSet: ChangeSet{Files: []FileChange{{RelativePath: "versions.txt", Kind: ChangeProviderDeclaration, Content: []byte("x")}}}, wantError: "must be a .tf"},
		{name: "unsupported kind", changeSet: ChangeSet{Files: []FileChange{{RelativePath: "file.tf", Kind: ChangeKind("other"), Content: []byte("x")}}}, wantError: "unsupported change kind"},
		{name: "blank content", changeSet: ChangeSet{Files: []FileChange{{RelativePath: "versions.tf", Kind: ChangeProviderDeclaration, Content: []byte("  \n")}}}, wantError: "is empty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateChangeSet(test.changeSet)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("ValidateChangeSet() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ValidateChangeSet() error = %v; want containing %q", err, test.wantError)
			}
		})
	}
}

func successfulReportForChangeSet() Report {
	steps := []StepResult{
		{Name: "init", Required: true},
		{Name: "validate", Required: true},
		{Name: "plan", Required: true},
		{Name: "show", Required: true},
	}
	return Report{
		Baseline:            ExecutionReport{Steps: steps, PlanAvailable: true},
		Upgraded:            ExecutionReport{Steps: steps, PlanAvailable: true},
		ComparisonAvailable: true,
	}
}
