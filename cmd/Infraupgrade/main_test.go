package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/infraupgrade/internal/analyzer"
	"github.com/asierhr/infraupgrade/internal/registry"
	"github.com/asierhr/infraupgrade/internal/scanner"
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

func TestGetUpgradePath(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantPath  string
		wantError bool
	}{
		{name: "default path", args: []string{"--dry-run"}, wantPath: "."},
		{name: "path before flag", args: []string{"project", "--dry-run"}, wantPath: "project"},
		{name: "flag before path", args: []string{"--dry-run", "project"}, wantPath: "project"},
		{name: "missing dry run", args: []string{"project"}, wantError: true},
		{name: "unknown option", args: []string{"--apply"}, wantError: true},
		{name: "two paths", args: []string{"one", "two", "--dry-run"}, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, err := getUpgradePath(test.args)
			if test.wantError {
				if err == nil {
					t.Fatalf("getUpgradePath() = %q, nil", path)
				}
				return
			}
			if err != nil || path != test.wantPath {
				t.Fatalf("getUpgradePath() = %q, %v; want %q", path, err, test.wantPath)
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

func TestPrintUsageListsEveryCommand(t *testing.T) {
	output := captureStdout(t, printUsage)
	for _, usageLine := range []string{
		"  infraupgrade scan [path]",
		"  infraupgrade check [path]",
		"  infraupgrade outdated [path]",
		"  infraupgrade upgrade [path] --dry-run",
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
		Steps: []upgrade.StepResult{step("init"), step("validate"), step("plan"), step("show")},
		Plan:  upgrade.PlanSummary{Create: 27},
	}
	report := upgrade.Report{
		Baseline: execution,
		Upgraded: upgrade.ExecutionReport{
			Name: "upgraded", PlanAvailable: true, Steps: execution.Steps,
			Plan: upgrade.PlanSummary{Create: 27},
		},
		ComparisonAvailable: true,
	}

	output := captureStdout(t, func() {
		printUpgradeReport(
			scanner.Result{Root: "."},
			[]registry.UpgradeCandidate{{
				Provider: "aws", CurrentVersion: "6.40.0", TargetVersion: "6.63.0", ChangeType: "minor",
			}},
			report,
		)
	})

	if !strings.Contains(output, "Differences: 0") || !strings.Contains(output, "Dry-run succeeded: true") {
		t.Fatalf("upgrade output = %q", output)
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
