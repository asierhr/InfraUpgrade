package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asierhr/infraupgrade/internal/migration"
	"github.com/asierhr/infraupgrade/internal/schemadiff"
)

func TestVerifyMigrationPlanMarksEquivalentMigrationAsVerified(t *testing.T) {
	project := migrationVerificationProject(t, `resource "example_resource" "example" {
  old_name = "value"
}
`)
	report := migrationVerificationReport(t)
	runner := &fakePlanRunner{}

	err := VerifyMigrationPlan(
		context.Background(),
		project,
		&report,
		runner,
		migrationVerificationTargets(),
	)
	if err != nil {
		t.Fatalf("VerifyMigrationPlan() error = %v", err)
	}

	proposal := report.MigrationPlan.Proposals[0]
	if proposal.Status != migration.ProposalVerified {
		t.Fatalf("proposal status = %q; want %q", proposal.Status, migration.ProposalVerified)
	}
	if proposal.Verification == nil || proposal.Verification.Risk != "low" {
		t.Fatalf("proposal verification = %#v; want low risk", proposal.Verification)
	}
	if !strings.Contains(proposal.Verification.Reason, "terraform validate and plan passed") {
		t.Fatalf("verification reason = %q", proposal.Verification.Reason)
	}
	if len(report.VerifiedMigrations) != 1 {
		t.Fatalf("verified migrations = %#v; want one", report.VerifiedMigrations)
	}
	verified := report.VerifiedMigrations[0]
	if verified.RelativePath != "main.tf" || verified.RuleID != "schema-derived" {
		t.Fatalf("verified migration = %#v", verified)
	}
	if strings.Contains(string(verified.Content), "old_name") ||
		!strings.Contains(string(verified.Content), `new_name = "value"`) {
		t.Fatalf("verified migration content is incorrect:\n%s", verified.Content)
	}
	if report.Upgraded.Name != "migration-verification" || !report.Upgraded.Succeeded() {
		t.Fatalf("effective upgraded execution = %#v", report.Upgraded)
	}
	if !report.ComparisonAvailable || report.Comparison.Risk() != "low" {
		t.Fatalf("effective comparison = %#v", report.Comparison)
	}

	content := readCandidateProjectFile(t, project, "main.tf")
	if strings.Contains(content, "new_name") || !strings.Contains(content, "old_name") {
		t.Fatalf("original project was modified:\n%s", content)
	}
}

func TestVerifyMigrationPlanCombinesCatalogAndInferredChanges(t *testing.T) {
	project := migrationVerificationProject(t, `resource "example_resource" "example" {
  old_name = "value"
}
`)
	report := migrationVerificationReport(t)
	report.AppliedMigrations = []AppliedMigration{{
		RelativePath: "main.tf",
		RuleID:       "catalog-rule",
		Description:  "add catalog setting",
		Content: []byte(`resource "example_resource" "example" {
  old_name     = "value"
  catalog_name = "catalog"
}
`),
	}}

	err := VerifyMigrationPlan(
		context.Background(),
		project,
		&report,
		&fakePlanRunner{},
		migrationVerificationTargets(),
	)
	if err != nil {
		t.Fatalf("VerifyMigrationPlan() error = %v", err)
	}
	if len(report.VerifiedMigrations) != 1 {
		t.Fatalf("verified migrations = %#v; want one combined file", report.VerifiedMigrations)
	}

	content := string(report.VerifiedMigrations[0].Content)
	for _, expected := range []string{`new_name`, `catalog_name = "catalog"`} {
		if !strings.Contains(content, expected) {
			t.Fatalf("combined migration does not contain %q:\n%s", expected, content)
		}
	}
	if strings.Contains(content, "old_name") {
		t.Fatalf("combined migration still contains old_name:\n%s", content)
	}
}

func TestVerifyMigrationPlanDoesNotPersistRejectedChanges(t *testing.T) {
	project := migrationVerificationProject(t, `resource "example_resource" "example" {
  old_name = "value"
}
`)
	report := migrationVerificationReport(t)
	originalUpgraded := report.Upgraded

	err := VerifyMigrationPlan(
		context.Background(),
		project,
		&report,
		&fakePlanRunner{differentPlan: true},
		migrationVerificationTargets(),
	)
	if err != nil {
		t.Fatalf("VerifyMigrationPlan() error = %v", err)
	}
	if len(report.VerifiedMigrations) != 0 {
		t.Fatalf("rejected changes were persisted: %#v", report.VerifiedMigrations)
	}
	if report.Upgraded.Name != originalUpgraded.Name {
		t.Fatalf("rejected verification replaced upgraded execution: %#v", report.Upgraded)
	}
}

func TestApplyExistingMigrationsRejectsPathOutsideWorkspace(t *testing.T) {
	err := applyExistingMigrations(t.TempDir(), []AppliedMigration{{
		RelativePath: "../outside.tf",
		Content:      []byte("content"),
	}})
	if err == nil || !strings.Contains(err.Error(), "leaves verification workspace") {
		t.Fatalf("applyExistingMigrations() error = %v; want traversal error", err)
	}
}

func TestVerifyMigrationPlanRejectsCandidateWhenApplicationFails(t *testing.T) {
	project := migrationVerificationProject(t, `resource "example_resource" "example" {
  another_name = "value"
}
`)
	report := migrationVerificationReport(t)
	runner := &countingRunner{delegate: &fakePlanRunner{}}

	err := VerifyMigrationPlan(
		context.Background(),
		project,
		&report,
		runner,
		migrationVerificationTargets(),
	)
	if err != nil {
		t.Fatalf("VerifyMigrationPlan() error = %v", err)
	}

	assertRejectedMigrationProposal(t, report, "source attribute")
	if runner.calls != 0 {
		t.Fatalf("runner calls = %d; want none after application failure", runner.calls)
	}
}

func TestVerifyMigrationPlanRejectsCandidateWhenValidateFails(t *testing.T) {
	project := migrationVerificationProject(t, `resource "example_resource" "example" {
  old_name = "value"
}
`)
	report := migrationVerificationReport(t)
	runner := &fakePlanRunner{
		failStep:      "validate",
		failExecution: "upgraded",
	}

	err := VerifyMigrationPlan(
		context.Background(),
		project,
		&report,
		runner,
		migrationVerificationTargets(),
	)
	if err != nil {
		t.Fatalf("VerifyMigrationPlan() error = %v", err)
	}

	assertRejectedMigrationProposal(t, report, "terraform validate failed")
}

func TestVerifyMigrationPlanRejectsCandidateWhenPlanIsNotEquivalent(t *testing.T) {
	project := migrationVerificationProject(t, `resource "example_resource" "example" {
  old_name = "value"
}
`)
	report := migrationVerificationReport(t)
	runner := &fakePlanRunner{differentPlan: true}

	err := VerifyMigrationPlan(
		context.Background(),
		project,
		&report,
		runner,
		migrationVerificationTargets(),
	)
	if err != nil {
		t.Fatalf("VerifyMigrationPlan() error = %v", err)
	}

	assertRejectedMigrationProposal(t, report, "migrated plan differs from baseline")
	verification := report.MigrationPlan.Proposals[0].Verification
	if verification == nil || verification.Risk != "high" {
		t.Fatalf("verification = %#v; want high risk", verification)
	}
}

func TestVerifyMigrationPlanRejectsUnexpectedProviderVersion(t *testing.T) {
	project := migrationVerificationProject(t, `resource "example_resource" "example" {
  old_name = "value"
}
`)
	report := migrationVerificationReport(t)
	targets := migrationVerificationTargets()
	targets[0].TargetVersion = "6.65.0"

	err := VerifyMigrationPlan(
		context.Background(),
		project,
		&report,
		&fakePlanRunner{},
		targets,
	)
	if err != nil {
		t.Fatalf("VerifyMigrationPlan() error = %v", err)
	}

	assertRejectedMigrationProposal(t, report, "instead of expected 6.65.0")
}

func TestVerifyMigrationPlanDoesNothingWithoutCandidates(t *testing.T) {
	project := migrationVerificationProject(t, testTerraformResource)
	report := Report{
		MigrationPlan: migration.Plan{
			Proposals: []migration.Proposal{{
				Status: migration.ProposalAmbiguous,
			}},
		},
	}
	runner := &countingRunner{delegate: &fakePlanRunner{}}

	if err := VerifyMigrationPlan(
		context.Background(),
		project,
		&report,
		runner,
		migrationVerificationTargets(),
	); err != nil {
		t.Fatalf("VerifyMigrationPlan() error = %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls = %d; want none", runner.calls)
	}
}

func TestVerifyMigrationPlanRejectsNilReport(t *testing.T) {
	err := VerifyMigrationPlan(
		context.Background(),
		t.TempDir(),
		nil,
		&fakePlanRunner{},
		migrationVerificationTargets(),
	)
	if err == nil || !strings.Contains(err.Error(), "report is nil") {
		t.Fatalf("VerifyMigrationPlan() error = %v; want nil report error", err)
	}
}

type countingRunner struct {
	delegate Runner
	calls    int
}

func (runner *countingRunner) Run(
	ctx context.Context,
	workingDirectory string,
	args ...string,
) (CommandResult, error) {
	runner.calls++
	return runner.delegate.Run(ctx, workingDirectory, args...)
}

func migrationVerificationProject(t *testing.T, terraform string) string {
	t.Helper()

	project := t.TempDir()
	mustWriteFile(t, project+"/main.tf", terraform)
	mustWriteFile(t, project+"/.terraform.lock.hcl", baselineTestLock)

	return project
}

func migrationVerificationReport(t *testing.T) Report {
	t.Helper()

	baselinePlan, err := ParsePlanToJSON([]byte(
		`{"resource_changes":[{"address":"aws_instance.web","mode":"managed","change":{"actions":["create"]}}]}`,
	))
	if err != nil {
		t.Fatalf("ParsePlanToJSON() error = %v", err)
	}

	candidate := migration.Candidate{
		ToPath:         "new_name",
		Transformation: []migration.TransformationKind{migration.RenameAttribute},
	}

	return Report{
		Baseline: ExecutionReport{
			Plan:          baselinePlan,
			PlanAvailable: true,
		},
		MigrationPlan: migration.Plan{
			Proposals: []migration.Proposal{{
				Assignment: schemadiff.Assignment{
					File:         "main.tf",
					ResourceType: "example_resource",
					ResourceName: "example",
					Attribute:    "old_name",
				},
				Status:     migration.ProposalCandidate,
				Candidates: []migration.Candidate{candidate},
				Selected:   &candidate,
			}},
		},
	}
}

func migrationVerificationTargets() []ProviderTarget {
	return []ProviderTarget{{
		Name:          "aws",
		Source:        "hashicorp/aws",
		TargetVersion: "6.64.0",
	}}
}

func assertRejectedMigrationProposal(t *testing.T, report Report, reason string) {
	t.Helper()

	proposal := report.MigrationPlan.Proposals[0]
	if proposal.Status != migration.ProposalRejected {
		t.Fatalf("proposal status = %q; want %q", proposal.Status, migration.ProposalRejected)
	}
	if proposal.Verification == nil || !strings.Contains(proposal.Verification.Reason, reason) {
		t.Fatalf("proposal verification = %#v; want reason containing %q", proposal.Verification, reason)
	}
}

func readCandidateProjectFile(t *testing.T, root string, relativePath string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relativePath)))
	if err != nil {
		t.Fatalf("read project file: %v", err)
	}

	return string(content)
}
