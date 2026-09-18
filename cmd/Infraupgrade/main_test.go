package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/infraupgrade/internal/analyzer"
	"github.com/asierhr/infraupgrade/internal/gitprepare"
	"github.com/asierhr/infraupgrade/internal/registry"
	"github.com/asierhr/infraupgrade/internal/scanner"
	"github.com/asierhr/infraupgrade/internal/schemadiff"
	"github.com/asierhr/infraupgrade/internal/upgrade"
)

func TestGetPathArgument(t *testing.T) {
	path, err := getPathArgument("scan", nil)
	if err != nil || path != "." {
		t.Fatalf("getPathArgument(nil) = %q, %v", path, err)
	}

	path, err = getPathArgument("scan", []string{"project"})
	if err != nil || path != "project" {
		t.Fatalf("getPathArgument(project) = %q, %v", path, err)
	}

	if _, err := getPathArgument("scan", []string{"one", "two"}); err == nil {
		t.Fatal("getPathArgument(two paths) error = nil")
	}
}

func TestGetUpgradeOptions(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantPath  string
		wantMode  upgradeMode
		wantError bool
	}{
		{name: "default path", args: []string{"--dry-run"}, wantPath: ".", wantMode: upgradeModeDryRun},
		{name: "path before flag", args: []string{"project", "--dry-run"}, wantPath: "project", wantMode: upgradeModeDryRun},
		{name: "flag before path", args: []string{"--prepare", "project"}, wantPath: "project", wantMode: upgradeModePrepare},
		{name: "pull request mode", args: []string{"--pr", "project"}, wantPath: "project", wantMode: upgradeModePullRequest},
		{name: "missing dry run", args: []string{"project"}, wantError: true},
		{name: "unknown option", args: []string{"--apply"}, wantError: true},
		{name: "two paths", args: []string{"one", "two", "--dry-run"}, wantError: true},
		{name: "two modes", args: []string{"--dry-run", "--prepare"}, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, err := getUpgradeOptions(test.args)
			if test.wantError {
				if err == nil {
					t.Fatalf("getUpgradeOptions() = %#v, nil", options)
				}
				return
			}
			if err != nil || options.Path != test.wantPath || options.Mode != test.wantMode {
				t.Fatalf("getUpgradeOptions() = %#v, %v; want path %q and mode %q", options, err, test.wantPath, test.wantMode)
			}
		})
	}
}

func TestFilterAvailableUpdates(t *testing.T) {
	candidates := []registry.UpgradeCandidate{
		{Provider: "aws", UpdateAvailable: true},
		{Provider: "random", UpdateAvailable: false},
	}

	updates := filterAvailableUpdates(candidates)
	if len(updates) != 1 || updates[0].Provider != "aws" {
		t.Fatalf("filterAvailableUpdates() = %#v", updates)
	}
}

func TestProviderTargets(t *testing.T) {
	targets := providerTargets([]registry.UpgradeCandidate{{
		Provider: "aws", Source: "hashicorp/aws", CurrentVersion: "6.40.0", TargetVersion: "6.64.0",
	}})
	if len(targets) != 1 || targets[0].Name != "aws" || targets[0].Source != "hashicorp/aws" ||
		targets[0].CurrentVersion != "6.40.0" || targets[0].TargetVersion != "6.64.0" {
		t.Fatalf("providerTargets() = %#v", targets)
	}
}

func TestPrepareNames(t *testing.T) {
	single := []registry.UpgradeCandidate{{Provider: "google/beta_provider", TargetVersion: "1.2.3 rc1"}}
	if got := prepareBranchName(single); got != "infraupgrade/google-beta-provider-1.2.3-rc1" {
		t.Fatalf("prepareBranchName(single) = %q", got)
	}
	if got := prepareCommitMessage(single); got != "chore(terraform): upgrade google/beta_provider provider to 1.2.3 rc1" {
		t.Fatalf("prepareCommitMessage(single) = %q", got)
	}
	if got := prepareBranchName([]registry.UpgradeCandidate{{}, {}}); got != "infraupgrade/provider-upgrades" {
		t.Fatalf("prepareBranchName(multiple) = %q", got)
	}
	if got := prepareCommitMessage([]registry.UpgradeCandidate{{}, {}}); got != "chore(terraform): upgrade providers" {
		t.Fatalf("prepareCommitMessage(multiple) = %q", got)
	}
}

func TestProviderDeclarationForUpdates(t *testing.T) {
	scanResult := scanner.Result{Providers: []scanner.Provider{
		{Name: "aws", Declared: false},
		{Name: "random", Declared: true},
	}}
	updates := []registry.UpgradeCandidate{
		{Provider: "aws", Source: "hashicorp/aws", TargetVersion: "6.64.0"},
		{Provider: "random", Source: "hashicorp/random", TargetVersion: "3.7.2"},
		{Provider: "azurerm", Source: "hashicorp/azurerm", TargetVersion: "4.0.0"},
	}

	declarations := providerDeclarationForUpdates(scanResult, updates)
	if len(declarations) != 2 {
		t.Fatalf("providerDeclarationForUpdates() = %#v", declarations)
	}
	if declarations[0].Name != "aws" || declarations[0].Constraint != "~> 6.64.0" {
		t.Fatalf("first declaration = %#v", declarations[0])
	}
	if declarations[1].Name != "azurerm" || declarations[1].Source != "hashicorp/azurerm" {
		t.Fatalf("second declaration = %#v", declarations[1])
	}
}

func TestPrintUsageListsEveryCommand(t *testing.T) {
	output := captureStdout(t, printUsage)
	for _, usageLine := range []string{
		"  infraupgrade scan [path]",
		"  infraupgrade check [path]",
		"  infraupgrade outdated [path]",
		"  infraupgrade upgrade [path] --dry-run",
		"  infraupgrade upgrade [path] --prepare",
		"  infraupgrade publish [path] --branch <branch> [--remote <remote>]",
		"  infraupgrade version",
	} {
		if !strings.Contains(output, usageLine) {
			t.Errorf("usage does not contain %q: %s", usageLine, output)
		}
	}
}

func TestPrintResults(t *testing.T) {
	scanResult := scanner.Result{
		Root:            ".",
		TerraformFiles:  []string{"main.tf"},
		RequiredVersion: ">= 1.6.0",
		Providers: []scanner.Provider{{
			Name:          "aws",
			Source:        "hashicorp/aws",
			Constraint:    "~> 6.0",
			LockedVersion: "6.40.0",
			Declared:      true,
			Configured:    true,
			Inferred:      true,
			ResourceCount: 1,
		}},
	}

	scanOutput := captureStdout(t, func() { printScanResult(scanResult) })
	if !strings.Contains(scanOutput, "aws (hashicorp/aws)") || !strings.Contains(scanOutput, "Resources: 1") {
		t.Fatalf("scan output = %q", scanOutput)
	}

	analysisOutput := captureStdout(t, func() {
		printAnalysisResult(scanResult, []analyzer.Finding{{
			Code: "TF001", Severity: "warning", Message: "message", Suggestion: "suggestion",
		}})
	})
	if !strings.Contains(analysisOutput, "TF001 [warning]") {
		t.Fatalf("analysis output = %q", analysisOutput)
	}

	outdatedOutput := captureStdout(t, func() {
		printOutdatedResult(scanResult, []registry.UpgradeCandidate{{
			Provider: "aws", Source: "hashicorp/aws", CurrentVersion: "6.40.0",
			LatestVersion: "6.63.0", LatestAllowedVersion: "6.63.0",
			TargetVersion: "6.63.0", ChangeType: "minor", UpdateAvailable: true,
		}})
	})
	if !strings.Contains(outdatedOutput, "Target: 6.63.0") {
		t.Fatalf("outdated output = %q", outdatedOutput)
	}
}

func TestPrintUpgradeReport(t *testing.T) {
	step := func(name string) upgrade.StepResult {
		return upgrade.StepResult{
			Name: name, Required: true,
			Command: upgrade.CommandResult{ExitCode: 0, Duration: time.Millisecond},
		}
	}
	execution := upgrade.ExecutionReport{
		Name: "baseline", PlanAvailable: true,
		Steps: []upgrade.StepResult{step("init"), step("schema"), step("validate"), step("plan"), step("show")},
		Plan: upgrade.PlanSummary{
			Create:               27,
			StateContext:         upgrade.StateContextExisting,
			PriorManagedResource: 27,
		},
	}
	report := upgrade.Report{
		Baseline: execution,
		Upgraded: upgrade.ExecutionReport{
			Name: "upgraded", PlanAvailable: true, Steps: execution.Steps,
			Plan: upgrade.PlanSummary{
				Create:               27,
				StateContext:         upgrade.StateContextExisting,
				PriorManagedResource: 27,
			},
		},
		ComparisonAvailable: true,
		Comparison: upgrade.PlanComparison{
			AttributeDifferences: []upgrade.AttributeDifference{{
				Address:    "aws_route.example",
				Phase:      "after",
				Path:       "odb_network_arn",
				Kind:       upgrade.AttributeAdded,
				SchemaOnly: true,
			}},
		},
	}
	versionChecks := []upgrade.ProviderVersionCheck{{
		Name:             "aws",
		Source:           "hashicorp/aws",
		ExpectedBaseline: "6.40.0",
		ActualBaseline:   "6.40.0",
		ExpectedUpgrade:  "6.63.0",
		ActualUpgrade:    "6.63.0",
		BaselineFound:    true,
		UpgradeFound:     true,
	}}
	decision := upgrade.EvaluateRecommendation(report, versionChecks)

	output := captureStdout(t, func() {
		printUpgradeReport(
			scanner.Result{Root: "."},
			[]registry.UpgradeCandidate{{
				Provider: "aws", CurrentVersion: "6.40.0", TargetVersion: "6.63.0", ChangeType: "minor",
			}},
			report,
			versionChecks,
			decision,
		)
	})

	for _, expected := range []string{
		"Action differences: 0",
		"Attribute differences: 1",
		"after.odb_network_arn added as null (schema-only)",
		"Dry-run succeeded: true",
		"Baseline selected: 6.40.0",
		"Upgrade selected:  6.63.0",
		"Verification: passed",
		"Validation context: existing-infrastructure",
		"Recommendation: safe",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("upgrade output does not contain %q: %s", expected, output)
		}
	}
}

func TestPrintCommandFailureUsesStderrThenStdout(t *testing.T) {
	stderrOutput := captureStdout(t, func() {
		printCommandFailure(upgrade.StepResult{Command: upgrade.CommandResult{
			ExitCode: 1, Stdout: "stdout", Stderr: "stderr",
		}})
	})
	if !strings.Contains(stderrOutput, "stderr") || strings.Contains(stderrOutput, "stdout") {
		t.Fatalf("failure output = %q", stderrOutput)
	}

	stdoutOutput := captureStdout(t, func() {
		printCommandFailure(upgrade.StepResult{Command: upgrade.CommandResult{
			ExitCode: 1, Stdout: "stdout",
		}})
	})
	if !strings.Contains(stdoutOutput, "stdout") {
		t.Fatalf("failure output = %q", stdoutOutput)
	}
}

func TestPrintPrepareResult(t *testing.T) {
	output := captureStdout(t, func() {
		printPrepareResult(gitprepare.Result{
			RepositoryRoot: `C:\repo`, Branch: "infraupgrade/aws-6.64.0", Commit: "abc123",
			ChangedFiles: []string{"Terraform/.terraform.lock.hcl", "Terraform/versions.tf"},
		})
	})
	for _, expected := range []string{
		`Repository: C:\repo`, "Branch: infraupgrade/aws-6.64.0", "Commit: abc123",
		"Terraform/.terraform.lock.hcl", "No changes were pushed.", "No pull request was created.",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("prepare output does not contain %q: %s", expected, output)
		}
	}
}

func TestPrintAssignmentChanges(t *testing.T) {
	tests := []struct {
		name     string
		report   upgrade.Report
		expected []string
	}{
		{
			name:     "unavailable",
			report:   upgrade.Report{},
			expected: []string{"Configuration assignment changes:", "unavailable"},
		},
		{
			name: "none",
			report: upgrade.Report{
				AssignmentAnalysisAvailable: true,
			},
			expected: []string{"Configuration assignment changes:", "none"},
		},
		{
			name: "nested type and nesting change",
			report: upgrade.Report{
				AssignmentAnalysisAvailable: true,
				AssignmentAnalysis: schemadiff.Report{Changes: []schemadiff.AssigmentChange{{
					Assignment: schemadiff.Assignment{
						File: "main.tf", ResourceType: "aws_instance", ResourceName: "web",
						BlockPath: []string{"root_block_device"}, Attribute: "volume_size", Expression: "var.disk_size",
					},
					Kind: schemadiff.NestingModeChanged, BeforeType: `"number"`, AfterType: `"string"`,
					BeforeNestingMode: "list", AfterNestingMode: "set",
				}}},
			},
			expected: []string{
				"aws_instance.web.root_block_device.volume_size", "File: main.tf", "Change: nesting-mode-changed",
				`Before type: "number"`, `After type:  "string"`, "Before nesting: list", "After nesting:  set",
				"Expression: var.disk_size",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := captureStdout(t, func() { printAssignmentChanges(test.report) })
			for _, expected := range test.expected {
				if !strings.Contains(output, expected) {
					t.Fatalf("assignment output does not contain %q: %s", expected, output)
				}
			}
		})
	}
}

func captureStdout(t *testing.T, action func()) string {
	t.Helper()

	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	os.Stdout = writer

	action()

	_ = writer.Close()
	os.Stdout = original
	content, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil {
		t.Fatalf("read captured stdout: %v", readErr)
	}
	return string(content)
}

func TestMergeAppliedMigrationsPrefersVerifiedFile(t *testing.T) {
	catalog := []upgrade.AppliedMigration{
		{RelativePath: "main.tf", RuleID: "catalog-main"},
		{RelativePath: "other.tf", RuleID: "catalog-other"},
	}
	verified := []upgrade.AppliedMigration{
		{RelativePath: ".\\main.tf", RuleID: "schema-derived"},
	}

	result := mergeAppliedMigrations(catalog, verified)
	if len(result) != 2 {
		t.Fatalf("mergeAppliedMigrations() = %#v; want two migrations", result)
	}
	if result[0].RuleID != "catalog-other" || result[1].RuleID != "schema-derived" {
		t.Fatalf("mergeAppliedMigrations() = %#v; verified file should replace catalog file", result)
	}
}

func TestGetPublishOptions(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		want      publishOptions
		wantError string
	}{
		{
			name: "defaults",
			args: []string{"--branch", "infraupgrade/aws-6.64.0"},
			want: publishOptions{Path: ".", Branch: "infraupgrade/aws-6.64.0", Remote: "origin"},
		},
		{
			name: "explicit values",
			args: []string{"Terraform", "--branch=infraupgrade/aws-6.64.0", "--remote", "upstream"},
			want: publishOptions{Path: "Terraform", Branch: "infraupgrade/aws-6.64.0", Remote: "upstream"},
		},
		{name: "missing branch", args: []string{"Terraform"}, wantError: "requires --branch"},
		{name: "branch value", args: []string{"--branch"}, wantError: "requires a value"},
		{name: "remote value", args: []string{"--branch=x", "--remote"}, wantError: "requires a value"},
		{name: "empty remote", args: []string{"--branch=x", "--remote="}, wantError: "cannot be empty"},
		{name: "unknown", args: []string{"--branch=x", "--draft"}, wantError: "unknown publish option"},
		{name: "two paths", args: []string{"one", "two", "--branch=x"}, wantError: "at most one path"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := getPublishOptions(test.args)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("getPublishOptions() = %#v, %v; want error containing %q", got, err, test.wantError)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("getPublishOptions() = %#v, %v; want %#v", got, err, test.want)
			}
		})
	}
}

func TestBuildPreparationInputCopiesUpgradeReport(t *testing.T) {
	updates := []registry.UpgradeCandidate{{
		Provider: "aws", Source: "hashicorp/aws", CurrentVersion: "6.40.0", TargetVersion: "6.64.0", ChangeType: "minor",
	}}
	report := upgrade.Report{
		Upgraded: upgrade.ExecutionReport{Plan: upgrade.PlanSummary{
			Create: 1, Update: 2, Replace: 3, Delete: 4, Read: 5, NoOp: 6, Unknown: 7,
		}},
		Comparison: upgrade.PlanComparison{
			Differences: []upgrade.ActionDifference{{
				Address: "aws_instance.web", BaselineAction: "update", UpgradedAction: "replace",
			}},
			AttributeDifferences: []upgrade.AttributeDifference{{
				Address: "aws_route.example", Phase: "after", Path: "new_field", Kind: upgrade.AttributeAdded, SchemaOnly: true, Sensitive: true,
			}},
		},
	}
	decision := upgrade.UpgradeDecision{
		Recommendation:    upgrade.RecommendationManualReview,
		ValidationContext: upgrade.StateContextExisting,
	}
	migrations := []upgrade.AppliedMigration{{
		RuleID: "schema-derived", RelativePath: "main.tf", Description: "rename field",
	}}

	input := buildPreparationInput(updates, report, decision, "upgrade aws", migrations)
	if len(input.Providers) != 1 || input.Providers[0].TargetVersion != "6.64.0" {
		t.Fatalf("providers = %#v", input.Providers)
	}
	wantPlan := gitprepare.PreparedPlan{Create: 1, Update: 2, Replace: 3, Delete: 4, Read: 5, NoOp: 6, Unknown: 7}
	if input.Plan != wantPlan {
		t.Fatalf("plan = %#v; want %#v", input.Plan, wantPlan)
	}
	if len(input.ActionDifferences) != 1 || input.ActionDifferences[0].UpgradedAction != "replace" {
		t.Fatalf("action differences = %#v", input.ActionDifferences)
	}
	if len(input.AttributeDifferences) != 1 || !input.AttributeDifferences[0].SchemaOnly || !input.AttributeDifferences[0].Sensitive {
		t.Fatalf("attribute differences = %#v", input.AttributeDifferences)
	}
	if len(input.Migrations) != 1 || input.Migrations[0].RuleID != "schema-derived" {
		t.Fatalf("migrations = %#v", input.Migrations)
	}
	if input.Validation.Risk != "high" || input.Validation.ActionDifferences != 1 || input.Validation.AttributeDifferences != 1 {
		t.Fatalf("validation = %#v", input.Validation)
	}
	if input.PullRequest.Title != "upgrade aws" {
		t.Fatalf("pull request = %#v", input.PullRequest)
	}
}

func TestPrintPublishedPullRequest(t *testing.T) {
	result := gitprepare.PullRequestResult{
		URL: "https://github.com/example/repository/pull/42", Branch: "infraupgrade/aws", BaseBranch: "main",
		Remote: "origin", Commit: "abc123", PreparationPath: `C:\repo\.git\infraupgrade\preparations\abc123.json`,
	}
	output := captureStdout(t, func() { printPublishedPullRequest(result) })
	for _, expected := range []string{"Pull request created:", result.URL, result.Branch, "Base branch: main", "Remote: origin", "Commit: abc123", result.PreparationPath} {
		if !strings.Contains(output, expected) {
			t.Fatalf("published PR output does not contain %q:\n%s", expected, output)
		}
	}

	result.AlreadyExisted = true
	output = captureStdout(t, func() { printPublishedPullRequest(result) })
	if !strings.Contains(output, "Pull request already exists:") {
		t.Fatalf("existing PR output = %q", output)
	}
}
