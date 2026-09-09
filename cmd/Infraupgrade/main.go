package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asierhr/infraupgrade/internal/analyzer"
	"github.com/asierhr/infraupgrade/internal/registry"
	"github.com/asierhr/infraupgrade/internal/scanner"
	"github.com/asierhr/infraupgrade/internal/upgrade"
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
		path, err := getUpgradePath(os.Args[2:])

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

		report, err := upgrade.DryRun(upgradeContext, path, upgrade.NewCommandRunner())

		if err != nil {
			fmt.Fprintf(os.Stderr, "upgrade dry-run failed: %v\n", err)

			os.Exit(1)
		}

		versionChecks := upgrade.VerifyProviderVersions(report, providerTargets(updates))

		printUpgradeReport(scanResult, updates, report, versionChecks)

		if !report.Succeeded() || !upgrade.AllVersionsCheckPassed(versionChecks) {
			os.Exit(2)
		}

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

		fmt.Printf(
			"    Declared: %t\n",
			provider.Declared,
		)

		fmt.Printf(
			"    Configured: %t\n",
			provider.Configured,
		)

		fmt.Printf(
			"    Inferred: %t\n",
			provider.Inferred,
		)

		fmt.Printf(
			"    Resources: %d\n",
			provider.ResourceCount,
		)
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

func getUpgradePath(args []string) (string, error) {
	path := "."
	pathSpecified := false
	dryRun := false

	for _, argument := range args {
		switch {
		case argument == "--dry-run":
			dryRun = true

		case strings.HasPrefix(argument, "-"):
			return "", fmt.Errorf("unknown upgrade option: %s", argument)

		case pathSpecified:
			return "", fmt.Errorf("upgrade accepts at most one path")

		default:
			path = argument
			pathSpecified = true
		}
	}

	if !dryRun {
		return "", fmt.Errorf("upgrade currently requires --dry-run")
	}

	return path, nil
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

func printUpgradeReport(scanResult scanner.Result, candidates []registry.UpgradeCandidate, report upgrade.Report, versionChecks []upgrade.ProviderVersionCheck) {

	fmt.Printf("Project: %s\n", filepath.Clean(scanResult.Root))

	fmt.Println("Upgrade simulation:")

	for _, candidate := range candidates {
		fmt.Printf("  - %s: %s -> %s (%s)\n", candidate.Provider, candidate.CurrentVersion, candidate.TargetVersion, candidate.ChangeType)
	}

	printExecutionReport(report.Baseline)
	printExecutionReport(report.Upgraded)

	if report.ComparisonAvailable {
		fmt.Println("\nUpgrade comparison:")

		fmt.Printf("  Differences: %d\n", len(report.Comparison.Differences))

		fmt.Printf("  Upgrade risk: %s\n", report.Comparison.Risk())

		for _, difference := range report.Comparison.Differences {
			fmt.Printf("  - %s: %s -> %s\n", difference.Address, difference.BaselineAction, difference.UpgradedAction)
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
