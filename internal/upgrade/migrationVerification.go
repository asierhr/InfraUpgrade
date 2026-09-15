package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asierhr/infraupgrade/internal/migration"
)

func VerifyMigrationPlan(ctx context.Context, projectRoot string, report *Report, runner Runner, targets []ProviderTarget) error {
	if report == nil {
		return fmt.Errorf("migration verification report is nil")
	}

	proposalIndexes := candidateProposalIndexes(report.MigrationPlan)

	if len(proposalIndexes) == 0 {
		return nil
	}

	workspace, cleanup, err := copyProject(projectRoot)

	if err != nil {
		return err
	}

	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove migration verification workspace: %w", cleanupErr))
		}
	}()

	if err := applyExistingMigrations(workspace, report.AppliedMigrations); err != nil {
		return fmt.Errorf("prepare catalog migrations for verification: %w", err)
	}

	proposals := make([]migration.Proposal, 0, len(proposalIndexes))

	for _, index := range proposalIndexes {
		proposals = append(proposals, report.MigrationPlan.Proposals[index])
	}

	changes, applyErr := migration.ApplyCandidate(workspace, proposals)

	if applyErr != nil {
		rejectProposals(&report.MigrationPlan, proposalIndexes, fmt.Sprintf("candidate application failed: %v", applyErr), "")

		return nil
	}

	originalLock, err := readOptionalFile(filepath.Join(projectRoot, ".terraform.lock.hcl"))

	if err != nil {
		return fmt.Errorf("read lockfile for migration verification: %w", err)
	}

	execution, executionErr := executeWorkspace(ctx, "migration-verification", workspace, originalLock, true, runner)

	if executionErr != nil {
		return fmt.Errorf("execute migration verification workspace: %w", executionErr)
	}

	if !execution.Succeeded() {
		rejectProposals(&report.MigrationPlan, proposalIndexes, failedExecutionReason(execution), "")
		return nil
	}

	if reason := verifySelectedTargetVersions(execution, targets); reason != "" {
		rejectProposals(&report.MigrationPlan, proposalIndexes, reason, "")
		return nil
	}

	if !report.Baseline.PlanAvailable {
		rejectProposals(&report.MigrationPlan, proposalIndexes, "baseline plan is unavailable", "")
		return nil
	}

	comparison := ComparePlans(report.Baseline.Plan, execution.Plan)

	risk := comparison.Risk()

	if risk != "low" {
		rejectProposals(&report.MigrationPlan, proposalIndexes,
			fmt.Sprintf(
				"migrated plan differs from baseline: %d action differences and %d attribute differences",
				len(comparison.Differences),
				len(comparison.AttributeDifferences),
			),
			risk,
		)

		return nil
	}

	verifyProposals(
		&report.MigrationPlan,
		proposalIndexes,
		fmt.Sprintf(
			"terraform validate and plan passed; migrated plan has %d action differences and %d schema-only attribute differences",
			len(comparison.Differences),
			len(comparison.AttributeDifferences),
		),
		risk,
	)

	report.VerifiedMigrations = convertVerifiedMigrations(changes)

	report.Upgraded = execution
	report.Comparison = comparison
	report.ComparisonAvailable = true

	return nil
}

func candidateProposalIndexes(plan migration.Plan) []int {
	var indexes []int

	for index, proposal := range plan.Proposals {
		if proposal.Status == migration.ProposalCandidate && proposal.Selected != nil {
			indexes = append(indexes, index)
		}
	}

	return indexes
}

func verifySelectedTargetVersions(execution ExecutionReport, targets []ProviderTarget) string {
	for _, target := range targets {
		selected, found := execution.SelectedVersions[target.Source]

		if !found {
			return fmt.Sprintf("provider %s was not selected in the verification workspace", target.Source)
		}

		if selected != target.TargetVersion {
			return fmt.Sprintf("provider %s selected version %s instead of expected %s", target.Source, selected, target.TargetVersion)
		}
	}

	return ""
}

func failedExecutionReason(execution ExecutionReport) string {
	for _, step := range execution.Steps {
		if !step.Required || step.Passed() {
			continue
		}

		output := step.Command.Stderr

		if output == "" {
			output = step.Command.Stdout
		}

		if output == "" {
			return fmt.Sprintf("terraform %s failed", step.Name)
		}

		return fmt.Sprintf("terraform %s failed: %s", step.Name, output)
	}

	return "migration verification did not produce a Terraform plan"
}

func verifyProposals(plan *migration.Plan, indexes []int, reason string, risk string) {
	for _, index := range indexes {
		plan.Proposals[index].Status = migration.ProposalVerified
		plan.Proposals[index].Verification = &migration.Verification{
			Reason: reason,
			Risk:   risk,
		}
	}
}

func rejectProposals(plan *migration.Plan, indexes []int, reason string, risk string) {
	for _, index := range indexes {
		plan.Proposals[index].Status = migration.ProposalRejected
		plan.Proposals[index].Verification = &migration.Verification{
			Reason: reason,
			Risk:   risk,
		}
	}
}

func convertVerifiedMigrations(changes []migration.FileChange) []AppliedMigration {
	result := make([]AppliedMigration, 0, len(changes))

	for _, change := range changes {
		result = append(result, AppliedMigration{
			RelativePath: change.RelativePath,
			RuleID:       change.RuleID,
			Description:  change.Description,
			Content: append(
				[]byte(nil),
				change.Content...,
			),
		})
	}
	return result
}

func applyExistingMigrations(workspace string, migrations []AppliedMigration) error {
	root, err := filepath.Abs(workspace)

	if err != nil {
		return fmt.Errorf("resolve verification workspace: %w", err)
	}

	for _, applied := range migrations {
		path, err := resolveVerificationPath(root, applied.RelativePath)

		if err != nil {
			return err
		}

		info, err := os.Stat(path)

		switch {
		case err == nil:
			if err := os.WriteFile(path, applied.Content, info.Mode().Perm()); err != nil {
				return fmt.Errorf("write catalog migration %s: %w", applied.RelativePath, err)
			}

		case os.IsNotExist(err):
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return fmt.Errorf("create migration directory: %w", err)
			}

			if err := os.WriteFile(path, applied.Content, 0o600); err != nil {
				return fmt.Errorf("create catalog migration %s: %w", applied.RelativePath, err)
			}

		default:
			return fmt.Errorf("inspect catalog migration %s: %w", applied.RelativePath, err)
		}
	}

	return nil
}

func resolveVerificationPath(root string, relativePath string) (string, error) {
	path, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(relativePath)))

	if err != nil {
		return "", fmt.Errorf("resolve migration path %s: %w", relativePath, err)
	}

	relative, err := filepath.Rel(root, path)

	if err != nil {
		return "", fmt.Errorf("validate migration path %s: %w", relativePath, err)
	}

	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("migration path leaves verification workspace: %s", relativePath)
	}

	return path, nil
}
