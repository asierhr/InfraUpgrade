package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asierhr/infraupgrade/internal/analyzer"
	"github.com/asierhr/infraupgrade/internal/gitprepare"
	"github.com/asierhr/infraupgrade/internal/migration"
	"github.com/asierhr/infraupgrade/internal/registry"
	"github.com/asierhr/infraupgrade/internal/scanner"
	"github.com/asierhr/infraupgrade/internal/upgrade"
)

type upgradeMode string

type upgradeOptions struct {
	Path string
	Mode upgradeMode
}

type publishOptions struct {
	Path   string
	Branch string
	Remote string
}

const (
	upgradeModeDryRun      upgradeMode = "dry-run"
	upgradeModePrepare     upgradeMode = "prepare"
	upgradeModePullRequest upgradeMode = "pr"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "scan":
		path, err := getPathArgument("scan", os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			printUsage()
			os.Exit(1)
		}

		result, err := scanner.Scan(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scan failed: %v\n", err)
			os.Exit(1)
		}

		printScanResult(result)

	case "check":
		path, err := getPathArgument("check", os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			printUsage()
			os.Exit(1)
		}

		result, err := scanner.Scan(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scan failed: %v\n", err)
			os.Exit(1)
		}

		findings := analyzer.Analyze(result)
		printAnalysisResult(result, findings)

	case "outdated":
		path, err := getPathArgument("outdated", os.Args[2:])

		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			printUsage()
			os.Exit(1)
		}

		result, err := scanner.Scan(path)

		if err != nil {
			fmt.Fprintf(os.Stderr, "scan failed: %v\n", err)
			os.Exit(1)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

		defer cancel()

		candidates, err := registry.FindUpdates(ctx, result, registry.NewClient())

		if err != nil {
			fmt.Fprintf(os.Stderr, "resolve updates failed: %v\n", err)
			os.Exit(1)
		}

		printOutdatedResult(result, candidates)

	case "upgrade":
		options, err := getUpgradeOptions(os.Args[2:])

		path := options.Path

		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			printUsage()
			os.Exit(1)
		}

		scanResult, err := scanner.Scan(path)

		if err != nil {
			fmt.Fprintf(os.Stderr, "scan failed: %v\n", err)
			os.Exit(1)
		}

		registryContext, cancelRegistry := context.WithTimeout(context.Background(), 30*time.Second)

		candidates, err := registry.FindUpdates(registryContext, scanResult, registry.NewClient())

		cancelRegistry()

		if err != nil {
			fmt.Fprintf(os.Stderr, "resolve updates failed: %v\n", err)
			os.Exit(1)
		}

		updates := filterAvailableUpdates(candidates)

		if len(updates) == 0 {
			fmt.Println("No provider updates available")
			return
		}

		upgradeContext, cancelUpgrade := context.WithTimeout(context.Background(), 10*time.Minute)

		defer cancelUpgrade()

		migrationEngine := migration.DefaultEngine()

		targets := providerTargets(updates)

		report, err := upgrade.DryRunWithMigrations(upgradeContext, path, upgrade.NewCommandRunner(), migrationEngine, targets)

		if err != nil {
			fmt.Fprintf(os.Stderr, "upgrade dry-run failed: %v\n", err)

			os.Exit(1)
		}

		if err := upgrade.VerifyMigrationPlan(upgradeContext, path, &report, upgrade.NewCommandRunner(), targets); err != nil {
			fmt.Fprintf(os.Stderr, "migration verification failed: %v\n", err)
			os.Exit(1)
		}

		versionChecks := upgrade.VerifyProviderVersions(report, targets)

		decision := upgrade.EvaluateRecommendation(report, versionChecks)

		printUpgradeReport(scanResult, updates, report, versionChecks, decision)

		printAssignmentChanges(report)

		printMigrationPlan(report)

		if !report.Succeeded() || !upgrade.AllVersionsCheckPassed(versionChecks) {
			os.Exit(2)
		}

		if options.Mode == upgradeModePrepare {
			if decision.Recommendation == upgrade.RecommendationBlocked {
				fmt.Fprintln(os.Stderr, "prepare blocked by upgradde recommendation")
				os.Exit(2)
			}

			declarations := providerDeclarationForUpdates(scanResult, updates)

			changeSet, err := upgrade.BuildPrepareChangeSet(report, path, declarations)

			if err != nil {
				fmt.Fprintf(os.Stderr, "build upgrade changeset: %v\n", err)

				os.Exit(1)
			}

			branch := prepareBranchName(updates)

			commitMessage := prepareCommitMessage(updates)

			prepareResult, err := gitprepare.Prepare(upgradeContext, path, changeSet, branch, commitMessage)

			if err != nil {
				fmt.Fprintf(os.Stderr, "prepare upgrade %v\n", err)

				os.Exit(1)
			}

			migrations := mergeAppliedMigrations(
				report.AppliedMigrations,
				report.VerifiedMigrations,
			)

			preparationInput := buildPreparationInput(
				updates,
				report,
				decision,
				commitMessage,
				migrations,
			)

			preparationPath, err := gitprepare.SavePreparation(
				upgradeContext,
				prepareResult,
				preparationInput,
			)

			if err != nil {
				fmt.Fprintf(os.Stderr, "save prepared upgrade: %v\n", err)

				os.Exit(1)
			}

			printPrepareResult(prepareResult)

			fmt.Printf("\nPreparation metadata: %s\n", preparationPath)

			printAppliedMigrations(mergeAppliedMigrations(report.AppliedMigrations, report.VerifiedMigrations))
		}

	case "publish":
		options, err := getPublishOptions(
			os.Args[2:],
		)

		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			printUsage()
			os.Exit(1)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)

		defer cancel()

		result, err := gitprepare.PublishPullRequest(
			ctx,
			options.Path,
			options.Branch,
			gitprepare.PublishOptions{
				Remote: options.Remote,
			},
		)

		if err != nil {
			fmt.Fprintf(os.Stderr, "publish prepared upgrade: %v\n", err)
			os.Exit(1)
		}

		printPublishedPullRequest(result)

	case "version":
		fmt.Println("infraupgrade development")

	default:
		fmt.Printf("Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  infraupgrade scan [path]")
	fmt.Println("  infraupgrade version")
	fmt.Println("  infraupgrade check [path]")
	fmt.Println("  infraupgrade outdated [path]")
	fmt.Println("  infraupgrade upgrade [path] --dry-run")
	fmt.Println("  infraupgrade upgrade [path] --prepare")
	fmt.Println("  infraupgrade publish [path] --branch <branch> [--remote <remote>]")
}

func printScanResult(result scanner.Result) {
	fmt.Printf("Project: %s\n", filepath.Clean(result.Root))
	fmt.Printf("Terraform files: %d\n", len(result.TerraformFiles))

	if result.RequiredVersion == "" {
		fmt.Println("Terraform version: not specified")
	} else {
		fmt.Printf("Terraform version: %s\n", result.RequiredVersion)
	}

	if len(result.Providers) == 0 {
		fmt.Println("Providers: none found")
		return
	}

	fmt.Println("Providers:")
	for _, provider := range result.Providers {
		fmt.Printf("  - %s", provider.Name)
		if provider.Source != "" {
			fmt.Printf(" (%s)", provider.Source)
		}
		fmt.Println()

		if provider.Constraint != "" {
			fmt.Printf("    Constraint: %s\n", provider.Constraint)
		} else {
			fmt.Println("    Constraint: not specified")
		}

		if provider.LockedVersion != "" {
			fmt.Printf("    Locked: %s\n", provider.LockedVersion)
		} else {
			fmt.Println("    Locked: not found")
		}

		fmt.Printf("    Declared: %t\n", provider.Declared)

		fmt.Printf("    Configured: %t\n", provider.Configured)

		fmt.Printf("    Inferred: %t\n", provider.Inferred)

		fmt.Printf("    Resources: %d\n", provider.ResourceCount)
	}
}

func getPathArgument(command string, args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("%s accepts at most one path", command)
	}

	if len(args) == 1 {
		return args[0], nil
	}

	return ".", nil
}

func printAnalysisResult(result scanner.Result, findings []analyzer.Finding) {
	fmt.Printf("Project: %s\n", filepath.Clean(result.Root))

	if len(findings) == 0 {
		fmt.Println("No findings")
		return
	}

	fmt.Printf("Findings: %d\n", len(findings))

	for _, finding := range findings {
		fmt.Printf("\n%s [%s]", finding.Code, finding.Severity)

		if finding.Provider != "" {
			fmt.Printf(" provider=%s", finding.Provider)
		}

		fmt.Println()
		fmt.Printf("  %s\n", finding.Message)
		fmt.Printf("  Suggestion: %s\n", finding.Suggestion)
	}
}

func printOutdatedResult(result scanner.Result, candidates []registry.UpgradeCandidate) {
	fmt.Printf(
		"Project: %s\n",
		filepath.Clean(result.Root),
	)

	if len(candidates) == 0 {
		fmt.Println("No comparable providers found")
		return
	}

	fmt.Println("Provider updates:")

	for _, candidate := range candidates {
		fmt.Printf("\n%s (%s)\n", candidate.Provider, candidate.Source)

		fmt.Printf("  Current: %s\n", candidate.CurrentVersion)

		if candidate.Constraint == "" {
			fmt.Println("  Constraint: not specified")
		} else {
			fmt.Printf("  Constraint: %s\n", candidate.Constraint)
		}

		fmt.Printf("  Latest: %s\n", candidate.LatestVersion)

		fmt.Printf("  Latest allowed: %s\n", candidate.LatestAllowedVersion)

		fmt.Printf("  Target: %s\n", candidate.TargetVersion)

		fmt.Printf("  Change: %s\n", candidate.ChangeType)

		fmt.Printf("  Update available: %t\n", candidate.UpdateAvailable)
	}
}

func getUpgradeOptions(args []string) (upgradeOptions, error) {
	options := upgradeOptions{
		Path: ".",
	}

	pathSpecified := false

	for _, argument := range args {

		switch {
		case argument == "--dry-run":
			if options.Mode != "" {
				return upgradeOptions{}, fmt.Errorf("upgrade accepts only one execution mode")
			}

			options.Mode = upgradeModeDryRun

		case argument == "--prepare":
			if options.Mode != "" {
				return upgradeOptions{}, fmt.Errorf("upgrade accepts only one execution mode")
			}

			options.Mode = upgradeModePrepare
		case argument == "--pr":
			if options.Mode != "" {
				return upgradeOptions{}, fmt.Errorf("upgrade accepts only one execution mode")
			}

			options.Mode = upgradeModePullRequest

		case strings.HasPrefix(argument, "-"):
			return upgradeOptions{}, fmt.Errorf("unknown upgrade option: %s", argument)

		case pathSpecified:
			return upgradeOptions{}, fmt.Errorf("upgrade accepts at most one path")

		default:
			options.Path = argument
			pathSpecified = true
		}
	}

	if options.Mode == "" {
		return upgradeOptions{}, fmt.Errorf("upgrade requires --dry-run or --prepare")
	}

	return options, nil
}

func filterAvailableUpdates(candidates []registry.UpgradeCandidate) []registry.UpgradeCandidate {
	var updates []registry.UpgradeCandidate

	for _, candidate := range candidates {
		if candidate.UpdateAvailable {
			updates = append(updates, candidate)
		}
	}

	return updates
}

func providerTargets(candidates []registry.UpgradeCandidate) []upgrade.ProviderTarget {
	targets := make([]upgrade.ProviderTarget, 0, len(candidates))

	for _, candidate := range candidates {
		targets = append(targets, upgrade.ProviderTarget{
			Name:           candidate.Provider,
			Source:         candidate.Source,
			CurrentVersion: candidate.CurrentVersion,
			TargetVersion:  candidate.TargetVersion,
		})
	}

	return targets
}

func printUpgradeReport(scanResult scanner.Result, candidates []registry.UpgradeCandidate, report upgrade.Report, versionChecks []upgrade.ProviderVersionCheck,
	decision upgrade.UpgradeDecision) {

	fmt.Printf("Project: %s\n", filepath.Clean(scanResult.Root))

	fmt.Println("Upgrade simulation:")

	for _, candidate := range candidates {
		fmt.Printf("  - %s: %s -> %s (%s)\n", candidate.Provider, candidate.CurrentVersion, candidate.TargetVersion, candidate.ChangeType)
	}

	printExecutionReport(report.Baseline)
	printExecutionReport(report.Upgraded)

	if report.ComparisonAvailable {
		fmt.Println("\nUpgrade comparison:")

		fmt.Printf("  Action differences: %d\n", len(report.Comparison.Differences))

		fmt.Printf("  Attribute differences: %d\n", len(report.Comparison.AttributeDifferences))

		fmt.Printf("  Upgrade risk: %s\n", report.Comparison.Risk())

		for _, difference := range report.Comparison.Differences {
			fmt.Printf("  - %s: action %s -> %s\n", difference.Address, difference.BaselineAction, difference.UpgradedAction)
		}

		for _, difference := range report.Comparison.AttributeDifferences {
			description := difference.Kind

			if difference.SchemaOnly {
				switch difference.Kind {
				case upgrade.AttributeAdded:
					description = "added as null (schema-only)"

				case upgrade.AttributeRemoved:
					description = "removed while null (schema-only)"
				}
			}

			sensitiveLabel := ""

			if difference.Sensitive {
				sensitiveLabel = " (sensitive value hidden)"
			}

			fmt.Printf("  - %s: %s.%s %s%s\n", difference.Address, difference.Phase, difference.Path, description, sensitiveLabel)
		}
	} else {
		fmt.Println("\nUpgrade comparison: unavailable")
	}

	fmt.Printf("\nDry-run succeeded: %t\n", report.Succeeded())

	fmt.Println("\nSelected provider versions:")

	for _, check := range versionChecks {
		status := "passed"

		if !check.Passed() {
			status = "failed"
		}

		fmt.Printf("\n  %s (%s):\n", check.Name, check.Source)

		if check.BaselineFound {
			fmt.Printf("    Baseline expected: %s\n", check.ExpectedBaseline)

			fmt.Printf("    Baseline selected: %s\n", check.ActualBaseline)
		} else {
			fmt.Println("    Baseline selected: not found")
		}

		if check.UpgradeFound {
			fmt.Printf("    Upgrade expected:  %s\n", check.ExpectedUpgrade)

			fmt.Printf("    Upgrade selected:  %s\n", check.ActualUpgrade)
		} else {
			fmt.Println("    Upgrade selected: not found")
		}

		fmt.Printf("    Verification: %s\n", status)
	}

	fmt.Printf("\nValidation context: %s\n", decision.ValidationContext)

	fmt.Printf("Recommendation: %s\n", decision.Recommendation)

	for _, reason := range decision.Reasons {
		fmt.Printf("  - %s\n", reason)
	}
}

func printExecutionReport(execution upgrade.ExecutionReport) {
	fmt.Printf("\n%s Terraform steps:\n", execution.Name)

	for _, step := range execution.Steps {
		status := "passed"

		if !step.Passed() {
			if step.Required {
				status = "failed"
			} else {
				status = "warning"
			}
		}

		fmt.Printf("  %-10s %s (%s)\n", step.Name+":", status, step.Command.Duration.Round(time.Millisecond))

		if !step.Passed() {
			printCommandFailure(step)
		}
	}

	if execution.PlanAvailable {
		fmt.Println("  Plan:")

		fmt.Printf("    Create:  %d\n", execution.Plan.Create)

		fmt.Printf("    Update:  %d\n", execution.Plan.Update)

		fmt.Printf("    Replace: %d\n", execution.Plan.Replace)

		fmt.Printf("    Delete:  %d\n", execution.Plan.Delete)

		fmt.Printf("  State context: %s\n", execution.Plan.StateContext)

		fmt.Printf("  Prior managed resources: %d\n", execution.Plan.PriorManagedResource)
	}

	fmt.Printf("  Lockfile changed: %t\n", execution.LockFileChanged)
}

func printCommandFailure(step upgrade.StepResult) {
	errorOutput := strings.TrimSpace(step.Command.Stderr)

	if errorOutput == "" {
		errorOutput = strings.TrimSpace(step.Command.Stdout)
	}

	if errorOutput == "" {
		return
	}

	fmt.Println("    Output:")

	for _, line := range strings.Split(errorOutput, "\n") {
		fmt.Printf("      %s\n", strings.TrimRight(line, "\r"))
	}
}

func prepareBranchName(updates []registry.UpgradeCandidate) string {
	if len(updates) == 1 {
		return fmt.Sprintf("infraupgrade/%s-%s", sanitizeGitName(updates[0].Provider), sanitizeGitName(updates[0].TargetVersion))
	}

	return "infraupgrade/provider-upgrades"
}

func prepareCommitMessage(updates []registry.UpgradeCandidate) string {
	if len(updates) == 1 {
		return fmt.Sprintf("chore(terraform): upgrade %s provider to %s", updates[0].Provider, updates[0].TargetVersion)
	}

	return "chore(terraform): upgrade providers"
}

func sanitizeGitName(value string) string {
	replacer := strings.NewReplacer(
		" ", "-",
		"/", "-",
		"\\", "-",
		"_", "-",
	)
	return replacer.Replace(value)
}

func printPrepareResult(result gitprepare.Result) {
	fmt.Println("\nUpgrade prepared locally:")

	fmt.Printf("  Repository: %s\n", result.RepositoryRoot)

	fmt.Printf("  Branch: %s\n", result.Branch)

	fmt.Printf("  Commit: %s\n", result.Commit)

	fmt.Println("  Changed files:")

	for _, file := range result.ChangedFiles {
		fmt.Printf("    - %s\n", file)
	}

	fmt.Println("\nNo changes were pushed.")
	fmt.Println("No pull request was created.")
}

func providerDeclarationForUpdates(scanResult scanner.Result, updates []registry.UpgradeCandidate) []upgrade.ProviderDeclaration {
	providersByName := make(map[string]scanner.Provider)

	for _, provider := range scanResult.Providers {
		providersByName[provider.Name] = provider
	}

	declarations := make([]upgrade.ProviderDeclaration, 0)

	for _, candidate := range updates {
		provider, exists := providersByName[candidate.Provider]

		if exists && provider.Declared {
			continue
		}

		declarations = append(declarations, upgrade.ProviderDeclaration{
			Name:       candidate.Provider,
			Source:     candidate.Source,
			Constraint: fmt.Sprintf("~> %s", candidate.TargetVersion),
		})
	}

	return declarations
}

func printAppliedMigrations(migrations []upgrade.AppliedMigration) {
	fmt.Println("\nApplied migrations:")

	if len(migrations) == 0 {
		fmt.Println("  none")
		return
	}

	for _, applied := range migrations {
		fmt.Printf("\n  - %s\n", applied.RuleID)
		fmt.Printf("    File: %s\n", applied.RelativePath)
		fmt.Printf("    Description: %s\n", applied.Description)
	}
}

func printAssignmentChanges(report upgrade.Report) {
	fmt.Println("\nConfiguration assignment changes:")

	if !report.AssignmentAnalysisAvailable {
		fmt.Println("  unavailable")
		return
	}

	if len(report.AssignmentAnalysis.Changes) == 0 {
		fmt.Println("  none")
		return
	}

	for _, change := range report.AssignmentAnalysis.Changes {
		assignment := change.Assignment

		fmt.Printf("\n  - %s.%s.%s\n", assignment.ResourceType, assignment.ResourceName, assignment.FullPath())

		fmt.Printf("    File: %s\n", assignment.File)

		fmt.Printf("    Change: %s\n", change.Kind)

		if change.BeforeType != "" {
			fmt.Printf("    Before type: %s\n", change.BeforeType)
		}

		if change.AfterType != "" {
			fmt.Printf("    After type:  %s\n", change.AfterType)
		}

		if change.BeforeNestingMode != "" {
			fmt.Printf("    Before nesting: %s\n", change.BeforeNestingMode)
		}

		if change.AfterNestingMode != "" {
			fmt.Printf("    After nesting:  %s\n", change.AfterNestingMode)
		}

		fmt.Printf("    Expression: %s\n", assignment.Expression)
	}
}

func mergeAppliedMigrations(catalog []upgrade.AppliedMigration, verified []upgrade.AppliedMigration) []upgrade.AppliedMigration {
	verifiedPaths := make(map[string]bool)

	for _, migration := range verified {
		verifiedPaths[filepath.ToSlash(filepath.Clean(migration.RelativePath))] = true
	}

	result := make([]upgrade.AppliedMigration, 0, len(catalog)+len(verified))

	for _, migration := range catalog {
		path := filepath.ToSlash(filepath.Clean(migration.RelativePath))

		if verifiedPaths[path] {
			continue
		}

		result = append(result, migration)
	}

	result = append(result, verified...)

	return result
}

func printMigrationPlan(report upgrade.Report) {
	fmt.Println("\nMigration proposals:")

	if !report.AssignmentAnalysisAvailable {
		fmt.Println("  unavailable")
		return
	}

	if len(report.MigrationPlan.Proposals) == 0 {
		fmt.Println("  none")
		return
	}

	for _, proposal := range report.MigrationPlan.Proposals {
		assignment := proposal.Assignment

		fmt.Printf("\n  - %s.%s.%s\n", assignment.ResourceType, assignment.ResourceName, assignment.FullPath())

		fmt.Printf("    File: %s\n", assignment.File)

		fmt.Printf("    Detected change: %s\n", proposal.DetectedChange)

		fmt.Printf("    Status: %s\n", proposal.Status)

		if proposal.Verification != nil {
			fmt.Printf("    Verification: %s\n", proposal.Verification.Reason)

			if proposal.Verification.Risk != "" {
				fmt.Printf("    Verification risk: %s\n", proposal.Verification.Risk)
			}
		}

		if len(proposal.Candidates) == 0 {
			fmt.Println("    Possible replacement: not found")

			continue
		}

		for index, candidate := range proposal.Candidates {

			fmt.Printf("\n    Candidate %d: %s\n", index+1, candidate.ToPath)

			fmt.Printf("      Before type: %s\n", candidate.BeforeType)

			fmt.Printf("      After type:  %s\n", candidate.AfterType)

			fmt.Printf("      Transformations: %s\n", formatTransformations(candidate.Transformation))

			fmt.Println("      Evidence:")

			for _, evidence := range candidate.Evidence {

				fmt.Printf("        - %s\n", evidence)
			}
		}
	}
}

func formatTransformations(transformations []migration.TransformationKind) string {
	values := make([]string, 0, len(transformations))

	for _, transformation := range transformations {
		values = append(values, string(transformation))
	}

	return strings.Join(values, ", ")
}

func buildPreparationInput(updates []registry.UpgradeCandidate, report upgrade.Report, decision upgrade.UpgradeDecision, title string, migrations []upgrade.AppliedMigration) gitprepare.PreparationInput {
	providers := make(
		[]gitprepare.PreparedProvider,
		0,
		len(updates),
	)

	for _, candidate := range updates {
		providers = append(providers, gitprepare.PreparedProvider{
			Name:            candidate.Provider,
			Source:          candidate.Source,
			PreviousVersion: candidate.CurrentVersion,
			TargetVersion:   candidate.TargetVersion,
			ChangeType:      string(candidate.ChangeType),
		})
	}

	actionDifferences := make([]gitprepare.PreparedActionDifference, 0, len(report.Comparison.Differences))

	for _, difference := range report.Comparison.Differences {
		actionDifferences = append(actionDifferences, gitprepare.PreparedActionDifference{
			Address:        difference.Address,
			BaselineAction: difference.BaselineAction,
			UpgradedAction: difference.UpgradedAction,
		})
	}

	attributeDifferences := make([]gitprepare.PreparedAttributeDifference, 0, len(report.Comparison.AttributeDifferences))

	for _, difference := range report.Comparison.AttributeDifferences {
		attributeDifferences = append(attributeDifferences, gitprepare.PreparedAttributeDifference{
			Address:    difference.Address,
			Phase:      difference.Phase,
			Path:       difference.Path,
			Kind:       difference.Kind,
			SchemaOnly: difference.SchemaOnly,
			Sensitive:  difference.Sensitive,
		})
	}

	preparedMigrations := make([]gitprepare.PreparedMigration, 0, len(migrations))

	for _, applied := range migrations {
		preparedMigrations = append(preparedMigrations, gitprepare.PreparedMigration{
			RuleID:       applied.RuleID,
			RelativePath: applied.RelativePath,
			Description:  applied.Description,
		})
	}

	plan := report.Upgraded.Plan

	return gitprepare.PreparationInput{
		Providers: providers,

		Plan: gitprepare.PreparedPlan{
			Create:  plan.Create,
			Update:  plan.Update,
			Replace: plan.Replace,
			Delete:  plan.Delete,
			Read:    plan.Read,
			NoOp:    plan.NoOp,
			Unknown: plan.Unknown,
		},

		ActionDifferences:    actionDifferences,
		AttributeDifferences: attributeDifferences,
		Migrations:           preparedMigrations,

		Validation: gitprepare.PreparedValidation{
			StateContext: string(
				decision.ValidationContext,
			),
			Recommendation: string(
				decision.Recommendation,
			),
			Risk: report.Comparison.Risk(),

			ActionDifferences: len(
				actionDifferences,
			),
			AttributeDifferences: len(
				attributeDifferences,
			),
		},

		PullRequest: gitprepare.PreparedPullRequest{
			Title: title,
		},
	}
}

func getPublishOptions(args []string) (publishOptions, error) {
	options := publishOptions{
		Path:   ".",
		Remote: "origin",
	}

	pathSpecified := false

	for index := 0; index < len(args); index++ {
		argument := args[index]

		switch {
		case argument == "--branch":
			if index+1 >= len(args) {
				return publishOptions{}, fmt.Errorf("--branch requires a value")
			}

			index++
			options.Branch = args[index]

		case strings.HasPrefix(argument, "--branch="):
			options.Branch = strings.TrimPrefix(argument, "--branch=")

		case argument == "--remote":
			if index+1 >= len(args) {
				return publishOptions{}, fmt.Errorf("--remote requires a value")
			}

			index++
			options.Remote = args[index]

		case strings.HasPrefix(argument, "--remote="):
			options.Remote = strings.TrimPrefix(argument, "--remote=")

		case strings.HasPrefix(argument, "-"):
			return publishOptions{}, fmt.Errorf("unknown publish option: %s", argument)

		case pathSpecified:
			return publishOptions{}, fmt.Errorf("publish accepts at most one path")

		default:
			options.Path = argument
			pathSpecified = true
		}
	}

	if strings.TrimSpace(options.Branch) == "" {
		return publishOptions{}, fmt.Errorf("publish requires --branch")
	}

	if strings.TrimSpace(options.Remote) == "" {
		return publishOptions{}, fmt.Errorf("publish remote cannot be empty")
	}

	return options, nil
}

func printPublishedPullRequest(result gitprepare.PullRequestResult) {
	if result.AlreadyExisted {
		fmt.Println("\nPull request already exists:")
	} else {
		fmt.Println("\nPull request created:")
	}

	fmt.Printf("  URL: %s\n", result.URL)
	fmt.Printf("  Branch: %s\n", result.Branch)
	fmt.Printf("  Base branch: %s\n", result.BaseBranch)
	fmt.Printf("  Remote: %s\n", result.Remote)
	fmt.Printf("  Commit: %s\n", result.Commit)
	fmt.Printf("  Preparation: %s\n", result.PreparationPath)
}
