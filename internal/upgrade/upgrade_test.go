package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const baselineTestLock = `provider "registry.terraform.io/hashicorp/aws" {
  version = "6.40.0"
}
`

const upgradedTestLock = `provider "registry.terraform.io/hashicorp/aws" {
  version = "6.64.0"
}
`

const baselineTestSchema = `{
  "format_version": "1.0",
  "provider_schemas": {
    "registry.terraform.io/hashicorp/aws": {
      "resource_schemas": {
        "aws_instance": {
          "block": {
            "attributes": {
              "ami": {"type": "string", "optional": true}
            }
          }
        }
      }
    }
  }
}`

const upgradedTestSchema = `{
  "format_version": "1.0",
  "provider_schemas": {
    "registry.terraform.io/hashicorp/aws": {
      "resource_schemas": {
        "aws_instance": {
          "block": {
            "attributes": {
              "ami": {"type": ["list", "string"], "optional": true}
            }
          }
        }
      }
    }
  }
}`

const testTerraformResource = `resource "aws_instance" "web" {
  ami = "ami-test"
}
`

type fakePlanRunner struct {
	workspaces     map[string]bool
	differentPlan  bool
	failStep       string
	failExecution  string
	runError       error
	baselineSchema string
	upgradedSchema string
}

func (fake *fakePlanRunner) Run(
	_ context.Context,
	workingDirectory string,
	args ...string,
) (CommandResult, error) {
	if fake.workspaces == nil {
		fake.workspaces = make(map[string]bool)
	}
	if fake.runError != nil {
		return CommandResult{}, fake.runError
	}

	command := args[0]
	if command == "init" {
		isUpgraded := containsString(args, "-upgrade")
		fake.workspaces[workingDirectory] = isUpgraded
		if isUpgraded {
			if err := os.WriteFile(
				filepath.Join(workingDirectory, ".terraform.lock.hcl"),
				[]byte(upgradedTestLock),
				0o600,
			); err != nil {
				return CommandResult{}, err
			}
		}
	}

	isUpgraded := fake.workspaces[workingDirectory]
	executionName := "baseline"
	if isUpgraded {
		executionName = "upgraded"
	}

	result := CommandResult{
		Args:     append([]string(nil), args...),
		ExitCode: 0,
	}
	if command == "providers" {
		result.Stdout = fake.baselineSchema
		if result.Stdout == "" {
			result.Stdout = baselineTestSchema
		}
		if isUpgraded && fake.upgradedSchema != "" {
			result.Stdout = fake.upgradedSchema
		}
	}
	if command == fake.failStep &&
		(fake.failExecution == "" || fake.failExecution == executionName) {
		result.ExitCode = 1
		result.Stderr = "simulated failure"
		return result, nil
	}

	if command == "show" {
		actions := `["create"]`
		if isUpgraded && fake.differentPlan {
			actions = `["delete","create"]`
		}
		result.Stdout = `{"resource_changes":[{"address":"aws_instance.web","mode":"managed","change":{"actions":` + actions + `}}]}`
	}

	return result, nil
}

func TestDryRunComparesBaselineAndUpgrade(t *testing.T) {
	project := t.TempDir()
	mustWriteFile(t, filepath.Join(project, "main.tf"), testTerraformResource)
	mustWriteFile(t, filepath.Join(project, ".terraform.lock.hcl"), baselineTestLock)
	runner := &fakePlanRunner{}

	report, err := DryRun(context.Background(), project, runner)
	if err != nil {
		t.Fatalf("DryRun() error = %v", err)
	}
	if !report.Succeeded() || !report.ComparisonAvailable {
		t.Fatalf("report = %#v, want successful comparison", report)
	}
	if report.Baseline.Name != "baseline" || report.Upgraded.Name != "upgraded" {
		t.Fatalf("execution names = %q, %q", report.Baseline.Name, report.Upgraded.Name)
	}
	if len(report.Baseline.Steps) != 6 || len(report.Upgraded.Steps) != 6 {
		t.Fatalf("step counts = %d, %d", len(report.Baseline.Steps), len(report.Upgraded.Steps))
	}
	if report.Baseline.LockFileChanged || !report.Upgraded.LockFileChanged {
		t.Fatalf("lock changes = baseline %t, upgraded %t", report.Baseline.LockFileChanged, report.Upgraded.LockFileChanged)
	}
	if len(report.Comparison.Differences) != 0 {
		t.Fatalf("differences = %#v, want none", report.Comparison.Differences)
	}
	if !report.AssignmentAnalysisAvailable || len(report.AssignmentAnalysis.Changes) != 0 {
		t.Fatalf("assignment analysis = %#v", report.AssignmentAnalysis)
	}
	if report.Baseline.SelectedVersions["hashicorp/aws"] != "6.40.0" {
		t.Fatalf("baseline selected versions = %#v", report.Baseline.SelectedVersions)
	}
	if report.Upgraded.SelectedVersions["hashicorp/aws"] != "6.64.0" {
		t.Fatalf("upgraded selected versions = %#v", report.Upgraded.SelectedVersions)
	}

	content, readErr := os.ReadFile(filepath.Join(project, ".terraform.lock.hcl"))
	if readErr != nil || string(content) != baselineTestLock {
		t.Fatalf("original lockfile = %q, %v", content, readErr)
	}
	for workspace := range runner.workspaces {
		assertPathMissing(t, filepath.Dir(workspace))
	}
}

func TestDryRunDetectsUpgradeDifference(t *testing.T) {
	project := t.TempDir()
	mustWriteFile(t, filepath.Join(project, "main.tf"), testTerraformResource)
	runner := &fakePlanRunner{differentPlan: true}

	report, err := DryRun(context.Background(), project, runner)
	if err != nil {
		t.Fatalf("DryRun() error = %v", err)
	}
	if len(report.Comparison.Differences) != 1 {
		t.Fatalf("differences = %#v, want one", report.Comparison.Differences)
	}
	if report.Comparison.Risk() != "high" {
		t.Fatalf("comparison risk = %q, want high", report.Comparison.Risk())
	}
}

func TestDryRunContinuesAfterFormattingWarning(t *testing.T) {
	project := t.TempDir()
	mustWriteFile(t, filepath.Join(project, "main.tf"), testTerraformResource)
	runner := &fakePlanRunner{failStep: "fmt"}

	report, err := DryRun(context.Background(), project, runner)
	if err != nil {
		t.Fatalf("DryRun() error = %v", err)
	}
	if !report.Succeeded() {
		t.Fatalf("report should succeed with formatting warnings: %#v", report)
	}
	if report.Baseline.Steps[2].Required || report.Baseline.Steps[2].Passed() {
		t.Fatalf("fmt step = %#v, want non-required warning", report.Baseline.Steps[2])
	}
}

func TestDryRunStopsExecutionAfterRequiredFailure(t *testing.T) {
	project := t.TempDir()
	mustWriteFile(t, filepath.Join(project, "main.tf"), testTerraformResource)
	runner := &fakePlanRunner{failStep: "validate", failExecution: "baseline"}

	report, err := DryRun(context.Background(), project, runner)
	if err != nil {
		t.Fatalf("DryRun() error = %v", err)
	}
	if report.Succeeded() || report.ComparisonAvailable {
		t.Fatalf("report = %#v, want unsuccessful report", report)
	}
	if len(report.Baseline.Steps) != 4 {
		t.Fatalf("baseline steps = %d, want 4", len(report.Baseline.Steps))
	}
	if !report.Upgraded.Succeeded() {
		t.Fatal("upgraded execution should still complete")
	}
}

func TestDryRunReturnsRunnerError(t *testing.T) {
	project := t.TempDir()
	mustWriteFile(t, filepath.Join(project, "main.tf"), testTerraformResource)

	_, err := DryRun(
		context.Background(),
		project,
		&fakePlanRunner{runError: errors.New("runner unavailable")},
	)
	if err == nil || !strings.Contains(err.Error(), "runner unavailable") {
		t.Fatalf("DryRun() error = %v, want runner error", err)
	}
}

func TestExecutionSucceededRequiresEveryRequiredStep(t *testing.T) {
	execution := ExecutionReport{
		PlanAvailable: true,
		Steps: []StepResult{
			{Name: "init", Required: true, Command: CommandResult{ExitCode: 0}},
			{Name: "schema", Required: true, Command: CommandResult{ExitCode: 0}},
			{Name: "validate", Required: true, Command: CommandResult{ExitCode: 0}},
			{Name: "plan", Required: true, Command: CommandResult{ExitCode: 0}},
		},
	}

	if execution.Succeeded() {
		t.Fatal("Succeeded() = true without show step")
	}
}

func TestDryRunDetectsAssignmentTypeChange(t *testing.T) {
	project := t.TempDir()
	mustWriteFile(t, filepath.Join(project, "main.tf"), testTerraformResource)

	report, err := DryRun(context.Background(), project, &fakePlanRunner{
		baselineSchema: baselineTestSchema,
		upgradedSchema: upgradedTestSchema,
	})
	if err != nil {
		t.Fatalf("DryRun() error = %v", err)
	}
	if !report.AssignmentAnalysisAvailable || len(report.AssignmentAnalysis.Changes) != 1 {
		t.Fatalf("assignment analysis = %#v", report.AssignmentAnalysis)
	}
	change := report.AssignmentAnalysis.Changes[0]
	if change.Assignment.Attribute != "ami" || change.BeforeType != `"string"` || change.AfterType != `["list","string"]` {
		t.Fatalf("assignment change = %#v", change)
	}
}

func TestDryRunRejectsEmptyProviderSchema(t *testing.T) {
	project := t.TempDir()
	mustWriteFile(t, filepath.Join(project, "main.tf"), testTerraformResource)

	runner := &emptySchemaRunner{fakePlanRunner: fakePlanRunner{}}
	_, err := DryRun(context.Background(), project, runner)
	if err == nil || !strings.Contains(err.Error(), "provider schema output is empty") {
		t.Fatalf("DryRun() error = %v", err)
	}
}

func TestDryRunPreservesProviderSchemaParseError(t *testing.T) {
	project := t.TempDir()
	mustWriteFile(t, filepath.Join(project, "main.tf"), testTerraformResource)

	_, err := DryRun(context.Background(), project, &fakePlanRunner{
		baselineSchema: baselineTestSchema,
		upgradedSchema: "not-json",
	})
	if err == nil || !strings.Contains(err.Error(), "decode provider schema JSON") {
		t.Fatalf("DryRun() error = %v; want provider schema decode error", err)
	}
}

type emptySchemaRunner struct {
	fakePlanRunner
}

func (runner *emptySchemaRunner) Run(ctx context.Context, workingDirectory string, args ...string) (CommandResult, error) {
	result, err := runner.fakePlanRunner.Run(ctx, workingDirectory, args...)
	if len(args) > 0 && args[0] == "providers" {
		result.Stdout = ""
	}
	return result, err
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
