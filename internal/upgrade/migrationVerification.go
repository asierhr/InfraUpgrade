package upgrade

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

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

	proposals := make([]migration.Proposal, 0, len(proposalIndexes))

	for _, index := range proposalIndexes {
		proposals = append(proposals, report.MigrationPlan.Proposals[index])
	}

	_, applyErr := migration.ApplyCandidate(workspace, proposals)

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
