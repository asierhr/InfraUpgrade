package gitprepare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const preparationFormatVersion = 1

var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

type PreparationInput struct {
	Providers []PreparedProvider

	Plan                 PreparedPlan
	ActionDifferences    []PreparedActionDifference
	AttributeDifferences []PreparedAttributeDifference
	Migrations           []PreparedMigration

	Validation  PreparedValidation
	PullRequest PreparedPullRequest
}

type PreparedProvider struct {
	Name            string `json:"name"`
	Source          string `json:"source"`
	PreviousVersion string `json:"previous_version"`
	TargetVersion   string `json:"target_version"`
	ChangeType      string `json:"change_type"`
}

type PreparedValidation struct {
	StateContext         string `json:"state_context"`
	Recommendation       string `json:"recommendation"`
	Risk                 string `json:"risk"`
	ActionDifferences    int    `json:"action_differences"`
	AttributeDifferences int    `json:"attribute_differences"`
}

type PreparedActionDifference struct {
	Address        string `json:"address"`
	BaselineAction string `json:"baseline_action"`
	UpgradedAction string `json:"upgraded_action"`
}

type PreparedAttributeDifference struct {
	Address    string `json:"address"`
	Phase      string `json:"phase"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	SchemaOnly bool   `json:"schema_only"`
	Sensitive  bool   `json:"sensitive"`
}

type PreparedPullRequest struct {
	Title string `json:"title"`
}

type PreparedPlan struct {
	Create  int `json:"create"`
	Update  int `json:"update"`
	Replace int `json:"replace"`
	Delete  int `json:"delete"`
	Read    int `json:"read"`
	NoOp    int `json:"no_op"`
	Unknown int `json:"unknown"`
}

type PreparedDifference struct {
	Address    string `json:"address"`
	Phase      string `json:"phase,omitempty"`
	Path       string `json:"path,omitempty"`
	Kind       string `json:"kind"`
	SchemaOnly bool   `json:"schema_only,omitempty"`
}

type PreparedMigration struct {
	RuleID       string `json:"rule_id"`
	RelativePath string `json:"relative_path"`
	Description  string `json:"description"`
}

type Preparation struct {
	FormatVersion int       `json:"format_version"`
	CreatedAt     time.Time `json:"created_at"`

	TerraformDirectory string `json:"terraform_directory"`
	Branch             string `json:"branch"`
	BaseBranch         string `json:"base_branch"`
	BaseCommit         string `json:"base_commit"`
	Commit             string `json:"commit"`

	Providers    []PreparedProvider `json:"providers"`
	ChangedFiles []string           `json:"changed_files"`

	Plan                 PreparedPlan                  `json:"plan"`
	ActionDifferences    []PreparedActionDifference    `json:"action_differences"`
	AttributeDifferences []PreparedAttributeDifference `json:"attribute_differences"`
	Migrations           []PreparedMigration           `json:"migrations"`

	Validation  PreparedValidation  `json:"validation"`
	PullRequest PreparedPullRequest `json:"pull_request"`
}

func SavePreparation(ctx context.Context, prepared Result, input PreparationInput) (string, error) {
	if err := validatePreparedResult(prepared); err != nil {
		return "", err
	}

	directory, err := preparationDirectory(ctx, prepared.RepositoryRoot)

	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create preparation directory: %w", err)
	}

	targetPath := filepath.Join(directory, strings.ToLower(prepared.Commit)+".json")

	if _, err := os.Stat(targetPath); err == nil {
		existing, loadErr := LoadPreparationByCommit(ctx, prepared.RepositoryRoot, prepared.Commit)

		if loadErr != nil {
			return "", loadErr
		}

		if existing.Branch != prepared.Branch {
			return "", fmt.Errorf("preparation for commit %s already belongs to branch %s", prepared.Commit, existing.Branch)
		}

		return targetPath, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect existing preparation: %w", err)
	}

	changedFiles := normalizedPreparationPaths(prepared.ChangedFiles)

	providers := append([]PreparedProvider(nil), input.Providers...)

	sort.Slice(providers, func(i, j int) bool {
		if providers[i].Source != providers[j].Source {
			return providers[i].Source < providers[j].Source
		}

		return providers[i].Name < providers[j].Name
	})

	preparation := Preparation{
		FormatVersion: preparationFormatVersion,
		CreatedAt:     time.Now().UTC(),

		TerraformDirectory: filepath.ToSlash(
			prepared.TerraformDirectory,
		),
		Branch:     prepared.Branch,
		BaseBranch: prepared.BaseBranch,
		BaseCommit: strings.ToLower(prepared.BaseCommit),
		Commit:     strings.ToLower(prepared.Commit),

		Providers: append(
			[]PreparedProvider(nil),
			providers...,
		),

		ChangedFiles: append(
			[]string(nil),
			changedFiles...,
		),

		Plan: input.Plan,

		ActionDifferences: append([]PreparedActionDifference(nil), input.ActionDifferences...),

		AttributeDifferences: append([]PreparedAttributeDifference(nil), input.AttributeDifferences...),

		Migrations: append([]PreparedMigration(nil), input.Migrations...),

		Validation: input.Validation,

		PullRequest: input.PullRequest,
	}

	if err := validatePreparation(preparation); err != nil {
		return "", err
	}

	content, err := json.MarshalIndent(preparation, "", " ")

	if err != nil {
		return "", fmt.Errorf("encode preparation: %w", err)
	}

	content = append(content, '\n')

	temporaryFile, err := os.CreateTemp(directory, ".preparation-*.json")

	if err != nil {
		return "", fmt.Errorf("create temporary preparation: %w", err)
	}

	temporaryPath := temporaryFile.Name()
	removeTemporary := true

	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporaryFile.Chmod(0o600); err != nil {
		_ = temporaryFile.Close()

		return "", fmt.Errorf("write preparation: %w", err)
	}

	if _, err := temporaryFile.Write(content); err != nil {
		_ = temporaryFile.Close()

		return "", fmt.Errorf("write preparation: %w", err)
	}

	if err := temporaryFile.Sync(); err != nil {
		_ = temporaryFile.Close()

		return "", fmt.Errorf("synchronize preparation: %w", err)
	}

	if err := temporaryFile.Close(); err != nil {
		return "", fmt.Errorf("close preparation: %w", err)
	}

	if err := os.Rename(temporaryPath, targetPath); err != nil {
		return "", fmt.Errorf("store preparation: %w", err)
	}

	removeTemporary = false

	return targetPath, nil
}

func LoadPreparationByBranch(ctx context.Context, repositoryRoot string, branch string) (Preparation, string, error) {
	if strings.TrimSpace(branch) == "" {
		return Preparation{}, "", fmt.Errorf("preparation branch is empty")
	}

	if _, err := runGit(ctx, repositoryRoot, "check-ref-format", "--branch", branch); err != nil {
		return Preparation{}, "", fmt.Errorf("invalid preparation branch %q: %w", branch, err)
	}

	commitOutput, err := runGit(ctx, repositoryRoot, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")

	if err != nil {
		return Preparation{}, "", fmt.Errorf("resolve prepared branch %s: %w", branch, err)
	}

	commit := strings.TrimSpace(commitOutput)

	preparation, err := LoadPreparationByCommit(ctx, repositoryRoot, commit)

	if err != nil {
		return Preparation{}, "", err
	}

	if preparation.Branch != branch {
		return Preparation{}, "", fmt.Errorf("preparation belongs to branch %s instead of %s", preparation.Branch, branch)
	}

	if preparation.Commit != strings.ToLower(commit) {
		return Preparation{}, "", fmt.Errorf("prepared branch changed after validation: expected %s, got %s", preparation.Commit, commit)
	}

	path, err := preparationPath(ctx, repositoryRoot, commit)

	if err != nil {
		return Preparation{}, "", err
	}

	return preparation, path, nil
}

func LoadPreparationByCommit(ctx context.Context, repositoryRoot string, commit string) (Preparation, error) {
	if !commitPattern.MatchString(commit) {
		return Preparation{}, fmt.Errorf("invalid preparation commit: %q", commit)
	}

	path, err := preparationPath(ctx, repositoryRoot, commit)

	if err != nil {
		return Preparation{}, err
	}

	content, err := os.ReadFile(path)

	if os.IsNotExist(err) {
		return Preparation{}, fmt.Errorf("no preparation found for commit %s", commit)
	}

	if err != nil {
		return Preparation{}, fmt.Errorf("read preparation: %w", err)
	}

	var preparation Preparation

	decoder := json.NewDecoder(bytes.NewReader(content))

	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&preparation); err != nil {
		return Preparation{}, fmt.Errorf("decode preparation: %w", err)
	}

	if err := validatePreparation(preparation); err != nil {
		return Preparation{}, err
	}

	if preparation.Commit != strings.ToLower(commit) {
		return Preparation{}, fmt.Errorf("preparation commit mismatch: expected %s, found %s", commit, preparation.Commit)
	}

	return preparation, nil
}

func VerifyPreparation(ctx context.Context, repositoryRoot string, preparation Preparation) error {
	if err := validatePreparation(preparation); err != nil {
		return err
	}

	branchCommitOutput, err := runGit(
		ctx,
		repositoryRoot,
		"rev-parse",
		"--verify",
		"refs/heads/"+preparation.Branch+"^{commit}",
	)

	if err != nil {
		return fmt.Errorf("resolve prepared branch: %w", err)
	}

	branchCommit := strings.ToLower(strings.TrimSpace(branchCommitOutput))

	if branchCommit != preparation.Commit {
		return fmt.Errorf("prepared branch changed after validation: expected %s, got %s", preparation.Commit, branchCommit)
	}

	parentOutput, err := runGit(
		ctx,
		repositoryRoot,
		"rev-parse",
		preparation.Commit+"^",
	)

	if err != nil {
		return fmt.Errorf("resolve prepared commit parent: %w", err)
	}

	parent := strings.ToLower(strings.TrimSpace(parentOutput))

	if parent != preparation.BaseCommit {
		return fmt.Errorf("prepared commit base changed: expected %s, got %s", preparation.BaseCommit, parent)
	}

	filesOutput, err := runGit(
		ctx,
		repositoryRoot,
		"diff-tree",
		"--root",
		"--no-commit-id",
		"--name-only",
		"-r",
		"-z",
		preparation.Commit,
	)

	if err != nil {
		return fmt.Errorf("read prepared commit files: %w", err)
	}

	actualFiles := parseNullSeparatedPaths(filesOutput)
	expectedFiles := normalizedPreparationPaths(preparation.ChangedFiles)

	if !equalStrings(actualFiles, expectedFiles) {
		return fmt.Errorf("prepared commit files changed: expected %v, got %v", expectedFiles, actualFiles)
	}

	return nil
}

func preparationPath(ctx context.Context, repositoryRoot string, commit string) (string, error) {
	directory, err := preparationDirectory(ctx, repositoryRoot)

	if err != nil {
		return "", err
	}

	return filepath.Join(directory, strings.ToLower(commit)+".json"), nil
}

func preparationDirectory(ctx context.Context, repositoryRoot string) (string, error) {
	output, err := runGit(
		ctx,
		repositoryRoot,
		"rev-parse",
		"--git-common-dir",
	)

	if err != nil {
		return "", fmt.Errorf("resolve git common directory: %w", err)
	}

	commonDirectory := filepath.Clean(strings.TrimSpace(output))

	if !filepath.IsAbs(commonDirectory) {
		commonDirectory = filepath.Join(repositoryRoot, commonDirectory)
	}

	commonDirectory, err = filepath.Abs(commonDirectory)

	if err != nil {
		return "", fmt.Errorf("resolve absolute git common directory: %w", err)
	}

	return filepath.Join(commonDirectory, "infraupgrade", "preparations"), nil
}

func validatePreparedResult(prepared Result) error {
	if strings.TrimSpace(prepared.RepositoryRoot) == "" {
		return fmt.Errorf("prepared result has no repository root")
	}

	if strings.TrimSpace(prepared.Branch) == "" {
		return fmt.Errorf("prepared result has no branch")
	}

	if !commitPattern.MatchString(prepared.Commit) {
		return fmt.Errorf("invalid prepared commit: %q", prepared.Commit)
	}

	if !commitPattern.MatchString(prepared.BaseCommit) {
		return fmt.Errorf("invalid prepared base commit: %q", prepared.BaseCommit)
	}

	return nil
}

func validatePreparation(preparation Preparation) error {
	if preparation.FormatVersion != preparationFormatVersion {
		return fmt.Errorf("unsupported preparation format: %d", preparation.FormatVersion)
	}

	if preparation.CreatedAt.IsZero() {
		return fmt.Errorf("preparation has no creation time")
	}

	if strings.TrimSpace(preparation.Branch) == "" {
		return fmt.Errorf("preparation has no branch")
	}

	if !commitPattern.MatchString(preparation.Commit) {
		return fmt.Errorf("invalid preparation commit: %q", preparation.Commit)
	}

	if !commitPattern.MatchString(preparation.BaseCommit) {
		return fmt.Errorf("invalid preparation base commit: %q", preparation.BaseCommit)
	}

	if strings.TrimSpace(preparation.PullRequest.Title) == "" {
		return fmt.Errorf("preparation has no pull request title")
	}

	if strings.TrimSpace(preparation.BaseBranch) == "" {
		return fmt.Errorf("preparation has no base branch")
	}

	return nil
}

func normalizedPreparationPaths(paths []string) []string {
	normalized := make([]string, 0, len(paths))
	seen := make(map[string]bool)

	for _, path := range paths {
		value := filepath.ToSlash(filepath.Clean(path))

		if value == "." || seen[value] {
			continue
		}

		seen[value] = true
		normalized = append(normalized, value)
	}

	sort.Strings(normalized)

	return normalized
}

func parseNullSeparatedPaths(output string) []string {
	parts := strings.Split(output, "\x00")
	paths := make([]string, 0, len(parts))

	for _, part := range parts {
		if part == "" {
			continue
		}

		paths = append(paths, filepath.ToSlash(filepath.Clean(part)))
	}

	sort.Strings(paths)

	return paths
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}

	return true
}
