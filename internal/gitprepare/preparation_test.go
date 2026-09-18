package gitprepare

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/infraupgrade/internal/upgrade"
)

func TestSaveLoadAndVerifyPreparation(t *testing.T) {
	repository, project, prepared := createPreparedUpgrade(t)
	input := completePreparationInput()

	path, err := SavePreparation(context.Background(), prepared, input)
	if err != nil {
		t.Fatalf("SavePreparation() error = %v", err)
	}
	wantDirectory := filepath.Join(repository, ".git", "infraupgrade", "preparations")
	if filepath.Dir(path) != wantDirectory {
		t.Fatalf("SavePreparation() path = %q; want directory %q", path, wantDirectory)
	}
	if filepath.Base(path) != strings.ToLower(prepared.Commit)+".json" {
		t.Fatalf("SavePreparation() filename = %q", filepath.Base(path))
	}

	loaded, loadedPath, err := LoadPreparationByBranch(
		context.Background(),
		repository,
		prepared.Branch,
	)
	if err != nil {
		t.Fatalf("LoadPreparationByBranch() error = %v", err)
	}
	if loadedPath != path {
		t.Fatalf("LoadPreparationByBranch() path = %q; want %q", loadedPath, path)
	}
	if loaded.Branch != prepared.Branch || loaded.BaseBranch != "main" || loaded.Commit != prepared.Commit {
		t.Fatalf("loaded preparation identity = %#v", loaded)
	}
	if loaded.TerraformDirectory != "Terraform" {
		t.Fatalf("loaded Terraform directory = %q", loaded.TerraformDirectory)
	}
	if !reflect.DeepEqual(loaded.ChangedFiles, prepared.ChangedFiles) {
		t.Fatalf("loaded changed files = %v; want %v", loaded.ChangedFiles, prepared.ChangedFiles)
	}
	if !reflect.DeepEqual(loaded.Plan, input.Plan) ||
		!reflect.DeepEqual(loaded.ActionDifferences, input.ActionDifferences) ||
		!reflect.DeepEqual(loaded.AttributeDifferences, input.AttributeDifferences) ||
		!reflect.DeepEqual(loaded.Migrations, input.Migrations) {
		t.Fatalf("loaded structured report differs: %#v", loaded)
	}
	if len(loaded.Providers) != 2 || loaded.Providers[0].Source != "hashicorp/aws" || loaded.Providers[1].Source != "hashicorp/random" {
		t.Fatalf("providers were not sorted: %#v", loaded.Providers)
	}
	if err := VerifyPreparation(context.Background(), repository, loaded); err != nil {
		t.Fatalf("VerifyPreparation() error = %v", err)
	}

	// Saving the same immutable commit again is idempotent.
	secondPath, err := SavePreparation(context.Background(), prepared, input)
	if err != nil || secondPath != path {
		t.Fatalf("second SavePreparation() = %q, %v; want %q", secondPath, err, path)
	}

	// The metadata is stored under .git and does not dirty the checkout.
	status := strings.TrimSpace(gitTest(t, repository, "status", "--porcelain=v1", "--untracked-files=all"))
	if status != "" {
		t.Fatalf("preparation metadata dirtied repository: %q", status)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(content, &raw); err != nil {
		t.Fatalf("saved preparation is not JSON: %v", err)
	}
	if raw["format_version"] != float64(preparationFormatVersion) {
		t.Fatalf("saved format version = %#v", raw["format_version"])
	}

	_ = project
}

func TestVerifyPreparationRejectsBranchMovedAfterValidation(t *testing.T) {
	repository, _, prepared := createPreparedUpgrade(t)
	path, err := SavePreparation(context.Background(), prepared, completePreparationInput())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPreparationByCommit(context.Background(), repository, prepared.Commit)
	if err != nil {
		t.Fatal(err)
	}

	gitTest(t, repository, "branch", "-f", prepared.Branch, prepared.BaseCommit)

	err = VerifyPreparation(context.Background(), repository, loaded)
	if err == nil || !strings.Contains(err.Error(), "branch changed after validation") {
		t.Fatalf("VerifyPreparation() error = %v; want moved branch error", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("preparation manifest unexpectedly missing: %v", statErr)
	}
}

func TestVerifyPreparationRejectsChangedFileManifest(t *testing.T) {
	repository, _, prepared := createPreparedUpgrade(t)
	_, err := SavePreparation(context.Background(), prepared, completePreparationInput())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPreparationByCommit(context.Background(), repository, prepared.Commit)
	if err != nil {
		t.Fatal(err)
	}
	loaded.ChangedFiles = []string{"Terraform/not-the-real-file.tf"}

	err = VerifyPreparation(context.Background(), repository, loaded)
	if err == nil || !strings.Contains(err.Error(), "commit files changed") {
		t.Fatalf("VerifyPreparation() error = %v; want changed files error", err)
	}
}

func TestLoadPreparationRejectsUnknownJSONFields(t *testing.T) {
	repository, _, prepared := createPreparedUpgrade(t)
	path, err := SavePreparation(context.Background(), prepared, completePreparationInput())
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "{", `{"unexpected":true,`, 1))
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = LoadPreparationByCommit(context.Background(), repository, prepared.Commit)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("LoadPreparationByCommit() error = %v; want unknown field error", err)
	}
}

func TestLoadPreparationRejectsMissingAndInvalidCommit(t *testing.T) {
	repository, _, _ := createPreparedUpgrade(t)

	if _, err := LoadPreparationByCommit(context.Background(), repository, "bad"); err == nil || !strings.Contains(err.Error(), "invalid preparation commit") {
		t.Fatalf("invalid commit error = %v", err)
	}
	missing := strings.Repeat("a", 40)
	if _, err := LoadPreparationByCommit(context.Background(), repository, missing); err == nil || !strings.Contains(err.Error(), "no preparation found") {
		t.Fatalf("missing preparation error = %v", err)
	}
}

func TestValidatePreparationRejectsIncompleteMetadata(t *testing.T) {
	valid := Preparation{
		FormatVersion: preparationFormatVersion,
		CreatedAt:     time.Now().UTC(),
		Branch:        "infraupgrade/test",
		BaseBranch:    "main",
		BaseCommit:    strings.Repeat("a", 40),
		Commit:        strings.Repeat("b", 40),
		PullRequest:   PreparedPullRequest{Title: "upgrade"},
	}

	tests := []struct {
		name   string
		mutate func(*Preparation)
		want   string
	}{
		{name: "format", mutate: func(value *Preparation) { value.FormatVersion = 2 }, want: "unsupported preparation format"},
		{name: "created", mutate: func(value *Preparation) { value.CreatedAt = time.Time{} }, want: "no creation time"},
		{name: "branch", mutate: func(value *Preparation) { value.Branch = "" }, want: "no branch"},
		{name: "base branch", mutate: func(value *Preparation) { value.BaseBranch = "" }, want: "no base branch"},
		{name: "base commit", mutate: func(value *Preparation) { value.BaseCommit = "bad" }, want: "invalid preparation base commit"},
		{name: "commit", mutate: func(value *Preparation) { value.Commit = "bad" }, want: "invalid preparation commit"},
		{name: "title", mutate: func(value *Preparation) { value.PullRequest.Title = "" }, want: "no pull request title"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			err := validatePreparation(value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validatePreparation() error = %v; want containing %q", err, test.want)
			}
		})
	}
}

func TestPreparationPathHelpers(t *testing.T) {
	paths := normalizedPreparationPaths([]string{"z.tf", `.\\a.tf`, "z.tf", "."})
	if !reflect.DeepEqual(paths, []string{"a.tf", "z.tf"}) {
		t.Fatalf("normalizedPreparationPaths() = %v", paths)
	}
	parsed := parseNullSeparatedPaths("z.tf\x00a.tf\x00")
	if !reflect.DeepEqual(parsed, []string{"a.tf", "z.tf"}) {
		t.Fatalf("parseNullSeparatedPaths() = %v", parsed)
	}
	if !equalStrings(parsed, []string{"a.tf", "z.tf"}) || equalStrings(parsed, []string{"a.tf"}) {
		t.Fatal("equalStrings() returned unexpected result")
	}
}

func createPreparedUpgrade(t *testing.T) (string, string, Result) {
	t.Helper()

	repository, project := createGitRepository(t)
	writeTestFile(t, filepath.Join(project, ".terraform.lock.hcl"), []byte("old lock\n"))
	gitTest(t, repository, "add", ".")
	gitTest(t, repository, "commit", "-m", "initial")

	result, err := Prepare(
		context.Background(),
		project,
		upgrade.ChangeSet{Files: []upgrade.FileChange{{
			RelativePath: ".terraform.lock.hcl",
			Kind:         upgrade.ChangeLockFile,
			Content:      []byte("new lock\n"),
		}}},
		"infraupgrade/aws-6.64.0",
		"upgrade aws provider",
	)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	return repository, project, result
}

func completePreparationInput() PreparationInput {
	return PreparationInput{
		Providers: []PreparedProvider{
			{Name: "random", Source: "hashicorp/random", PreviousVersion: "3.6.0", TargetVersion: "3.7.0", ChangeType: "minor"},
			{Name: "aws", Source: "hashicorp/aws", PreviousVersion: "6.40.0", TargetVersion: "6.64.0", ChangeType: "minor"},
		},
		Plan: PreparedPlan{Create: 1, Update: 2, Replace: 3, Delete: 4, Read: 5, NoOp: 6, Unknown: 7},
		ActionDifferences: []PreparedActionDifference{{
			Address: "aws_instance.web", BaselineAction: "update", UpgradedAction: "replace",
		}},
		AttributeDifferences: []PreparedAttributeDifference{{
			Address: "aws_instance.web", Phase: "after", Path: "new_field", Kind: "added", SchemaOnly: true,
		}},
		Migrations: []PreparedMigration{{RuleID: "schema-derived", RelativePath: "main.tf", Description: "rename field"}},
		Validation: PreparedValidation{
			StateContext: "existing-infrastructure", Recommendation: "manual-review", Risk: "medium",
			ActionDifferences: 1, AttributeDifferences: 1,
		},
		PullRequest: PreparedPullRequest{Title: "upgrade providers"},
	}
}
