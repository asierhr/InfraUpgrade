package upgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asierhr/infraupgrade/internal/migration"
	"github.com/asierhr/infraupgrade/internal/scanner"
	"github.com/asierhr/infraupgrade/internal/schemadiff"
)

type StepResult struct {
	Name     string
	Required bool
	Command  CommandResult
}

type ExecutionReport struct {
	Name               string
	Steps              []StepResult
	Plan               PlanSummary
	PlanAvailable      bool
	LockFileChanged    bool
	LockFileContent    []byte
	SelectedVersions   map[string]string
	ProviderSchemaJSON []byte
}

type Report struct {
	ProjectRoot         string
	Baseline            ExecutionReport
	Upgraded            ExecutionReport
	Comparison          PlanComparison
	ComparisonAvailable bool
	AppliedMigrations   []AppliedMigration

	AssignmentAnalysis          schemadiff.Report
	AssignmentAnalysisAvailable bool

	MigrationPlan migration.Plan
}

type stepDefinition struct {
	name              string
	args              []string
	continueOnFailure bool
}

func (step StepResult) Passed() bool {
	return step.Command.ExitCode == 0
}

func (execution ExecutionReport) Succeeded() bool {
	requiredSteps := map[string]bool{
		"init":     false,
		"schema":   false,
		"validate": false,
		"plan":     false,
		"show":     false,
	}

	for _, step := range execution.Steps {
		if !step.Required {
			continue
		}

		if !step.Passed() {
			return false
		}

		requiredSteps[step.Name] = true
	}

	for _, executed := range requiredSteps {
		if !executed {
			return false
		}
	}

	return execution.PlanAvailable
}

func (report Report) Succeeded() bool {
	return report.Baseline.Succeeded() && report.Upgraded.Succeeded() && report.ComparisonAvailable
}

func DryRun(ctx context.Context, projectRoot string, runner Runner) (Report, error) {
	return dryRun(ctx, projectRoot, runner, nil)
}

func dryRun(ctx context.Context, projectRoot string, runner Runner, migrate workspaceMigrator) (report Report, err error) {
	absoluteRoot, err := filepath.Abs(projectRoot)

	if err != nil {
		return Report{}, fmt.Errorf("resolve project path: %w", err)
	}

	originalLock, err := readOptionalFile(filepath.Join(absoluteRoot, ".terraform.lock.hcl"))

	if err != nil {
		return Report{}, fmt.Errorf("read original lockfile: %w", err)
	}

	baselineWorkspace, cleanupBaseline, err := copyProject(absoluteRoot)

	if err != nil {
		return Report{}, err
	}

	defer func() {
		cleanupError := cleanupBaseline()

		if cleanupError != nil {
			err = errors.Join(err, fmt.Errorf("remove temporary workspace: %w", cleanupError))
		}
	}()

	upgradedWorkspace, cleanupUpgraded, err := copyProject(absoluteRoot)

	if err != nil {
		return Report{}, err
	}

	defer func() {
		cleanupError := cleanupUpgraded()

		if cleanupError != nil {
			err = errors.Join(
				err,
				fmt.Errorf(
					"remove upgraded workspace: %w",
					cleanupError,
				),
			)
		}
	}()

	report.ProjectRoot = absoluteRoot

	report.Baseline, err = executeWorkspace(ctx, "baseline", baselineWorkspace, originalLock, false, runner)

	if err != nil {
		return report, err
	}

	if migrate != nil {
		report.AppliedMigrations, err = migrate(upgradedWorkspace)

		if err != nil {
			return report, fmt.Errorf("migrate upgraded workspace: %w", err)
		}
	}

	report.Upgraded, err = executeWorkspace(ctx, "upgraded", upgradedWorkspace, originalLock, true, runner)

	if err != nil {
		return report, err
	}

	if len(report.Baseline.ProviderSchemaJSON) > 0 && len(report.Upgraded.ProviderSchemaJSON) > 0 {
		assigmentAnalysis, detectErr := schemadiff.Detect(absoluteRoot, report.Baseline.ProviderSchemaJSON, report.Upgraded.ProviderSchemaJSON)

		if detectErr != nil {
			return report, fmt.Errorf("detect assignment schema changes: %w", detectErr)
		}

		report.AssignmentAnalysis = assigmentAnalysis
		report.AssignmentAnalysisAvailable = true

		report.MigrationPlan = migration.Build(assigmentAnalysis)
	}

	if report.Baseline.PlanAvailable && report.Upgraded.PlanAvailable {
		report.Comparison = ComparePlans(report.Baseline.Plan, report.Upgraded.Plan)

		report.ComparisonAvailable = true
	}

	return report, nil
}

func executeWorkspace(ctx context.Context, name string, workspace string, originalLock []byte, upgradeProviders bool, runner Runner) (ExecutionReport, error) {
	execution := ExecutionReport{
		Name: name,
	}

	initArguments := []string{
		"init",
		"-backend=false",
		"-input=false",
	}

	if upgradeProviders {
		initArguments = append(initArguments, "-upgrade")
	}

	initArguments = append(initArguments, "-no-color")

	steps := []stepDefinition{
		{
			name: "init",
			args: initArguments,
		},
		{
			name: "schema",
			args: []string{
				"providers",
				"schema",
				"-json",
			},
		},
		{
			name: "fmt",
			args: []string{
				"fmt",
				"-check",
				"-recursive",
			},
			continueOnFailure: true,
		},
		{
			name: "validate",
			args: []string{
				"validate",
				"-no-color",
			},
		},
		{
			name: "plan",
			args: []string{
				"plan",
				"-input=false",
				"-lock=false",
				"-refresh=false",
				"-no-color",
				"-out=infraupgrade.tfplan",
			},
		},
		{
			name: "show",
			args: []string{
				"show",
				"-json",
				"infraupgrade.tfplan",
			},
		},
	}

	for _, step := range steps {
		commandResult, runError := runner.Run(ctx, workspace, step.args...)

		if runError != nil {
			return execution, fmt.Errorf("run %s terraform %s: %w", name, step.name, runError)
		}

		stepResult := StepResult{
			Name:     step.name,
			Required: !step.continueOnFailure,
			Command:  commandResult,
		}

		execution.Steps = append(execution.Steps, stepResult)

		if step.name == "show" && stepResult.Passed() {
			planSummary, parseError := ParsePlanToJSON([]byte(commandResult.Stdout))

			if parseError != nil {
				return execution, parseError
			}

			execution.Plan = planSummary
			execution.PlanAvailable = true
		}

		if step.name == "schema" && stepResult.Passed() {
			if len(bytes.TrimSpace([]byte(commandResult.Stdout))) == 0 {
				return execution, fmt.Errorf("%s provider schema output is empty", name)
			}

			execution.ProviderSchemaJSON = append([]byte(nil), []byte(commandResult.Stdout)...)
		}

		if !stepResult.Passed() && !step.continueOnFailure {
			break
		}
	}

	updatedLock, err := readOptionalFile(filepath.Join(workspace, ".terraform.lock.hcl"))

	if err != nil {
		return execution, fmt.Errorf("read %s lockfile: %w", name, err)
	}

	execution.LockFileChanged = !bytes.Equal(originalLock, updatedLock)

	execution.LockFileContent = append([]byte(nil), updatedLock...)

	selectedVersions, err := scanner.ReadLockedVersion(workspace)

	if err != nil {
		return execution, fmt.Errorf("read %s selected provider versions: %w", name, err)
	}

	execution.SelectedVersions = selectedVersions

	return execution, nil
}

func readOptionalFile(path string) ([]byte, error) {
	content, err := os.ReadFile(path)

	if os.IsNotExist(err) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return content, nil
}
