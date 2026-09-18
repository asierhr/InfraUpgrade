package gitprepare

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type PublishOptions struct {
	Remote string
}

type PullRequestResult struct {
	URL             string
	Branch          string
	BaseBranch      string
	Remote          string
	Commit          string
	PreparationPath string
	AlreadyExisted  bool
}

func PublishPullRequest(ctx context.Context, projectRoot string, branch string, options PublishOptions) (PullRequestResult, error) {
	if strings.TrimSpace(projectRoot) == "" {
		projectRoot = "."
	}

	absoluteProject, err := filepath.Abs(projectRoot)

	if err != nil {
		return PullRequestResult{}, fmt.Errorf("resolve project path: %w", err)
	}

	repositoryOutput, err := runGit(ctx, absoluteProject, "rev-parse", "--show-toplevel")

	if err != nil {
		return PullRequestResult{}, fmt.Errorf("find git repository: %w", err)
	}

	repositoryRoot := filepath.Clean(strings.TrimSpace(repositoryOutput))

	preparation, preparationPath, err := LoadPreparationByBranch(ctx, repositoryRoot, branch)

	if err != nil {
		return PullRequestResult{}, err
	}

	if err := verifyTerraformDirectory(repositoryRoot, absoluteProject, preparation.TerraformDirectory); err != nil {
		return PullRequestResult{}, err
	}

	if err := VerifyPreparation(ctx, repositoryRoot, preparation); err != nil {
		return PullRequestResult{}, fmt.Errorf("verify prepared upgrade: %w", err)
	}

	remote := strings.TrimSpace(options.Remote)

	if remote == "" {
		remote = "origin"
	}

	if _, err := runGit(ctx, repositoryRoot, "remote", "get-url", remote); err != nil {
		return PullRequestResult{}, fmt.Errorf("resolve remote %s: %w", remote, err)
	}

	if _, err := runGH(ctx, repositoryRoot, "auth", "status"); err != nil {
		return PullRequestResult{}, fmt.Errorf("GitHub CLI authentication failed: %w", err)
	}

	if _, err := runGit(ctx, repositoryRoot, "push", "--set-upstream", remote, "refs/heads/"+preparation.Branch+":refs/heads/"+preparation.Branch); err != nil {
		return PullRequestResult{}, fmt.Errorf("push prepared branch %s: %w", preparation.Branch, err)
	}

	existingURL, existing := findExistingPullRequest(ctx, repositoryRoot, preparation.Branch)

	if existing {
		return PullRequestResult{
			URL:             existingURL,
			Branch:          preparation.Branch,
			BaseBranch:      preparation.BaseBranch,
			Remote:          remote,
			Commit:          preparation.Commit,
			PreparationPath: preparationPath,
			AlreadyExisted:  true,
		}, nil
	}

	pullRequestBody := BuildPullRequestBody(preparation)

	bodyPath, cleanupBody, err := writePullRequestBody(pullRequestBody)

	if err != nil {
		return PullRequestResult{}, err
	}

	defer cleanupBody()

	arguments := []string{
		"pr",
		"create",
		"--head",
		preparation.Branch,
		"--base",
		preparation.BaseBranch,
		"--title",
		preparation.PullRequest.Title,
		"--body-file",
		bodyPath,
	}

	output, err := runGH(ctx, repositoryRoot, arguments...)

	if err != nil {
		return PullRequestResult{}, fmt.Errorf("create pull request; branch %s was pushed but the PR was not created: %w", preparation.Branch, err)
	}

	pullRequestURL := lastNonEmptyLine(output)

	if err := validatePullRequestURL(pullRequestURL); err != nil {
		return PullRequestResult{}, err
	}

	return PullRequestResult{
		URL:             pullRequestURL,
		Branch:          preparation.Branch,
		BaseBranch:      preparation.BaseBranch,
		Remote:          remote,
		Commit:          preparation.Commit,
		PreparationPath: preparationPath,
		AlreadyExisted:  false,
	}, nil
}

func verifyTerraformDirectory(repositoryRoot string, projectRoot string, expected string) error {
	relative, err := filepath.Rel(repositoryRoot, projectRoot)

	if err != nil {
		return fmt.Errorf("resolve Terraform directory: %w", err)
	}

	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf(
			"Terraform directory is outside the repository",
		)
	}

	actual := filepath.ToSlash(filepath.Clean(relative))

	expected = filepath.ToSlash(filepath.Clean(expected))

	if actual != expected {
		return fmt.Errorf("preparation belongs to Terraform directory %s instead of %s", expected, actual)
	}

	return nil
}

func findExistingPullRequest(ctx context.Context, repositoryRoot string, branch string) (string, bool) {
	output, err := runGH(
		ctx,
		repositoryRoot,
		"pr",
		"view",
		branch,
		"--json",
		"url",
		"--jq",
		".url",
	)

	if err != nil {
		return "", false
	}

	value := strings.TrimSpace(output)

	if validatePullRequestURL(value) != nil {
		return "", false
	}

	return value, true
}

func writePullRequestBody(body string) (string, func(), error) {
	file, err := os.CreateTemp("", "infraupgrade-pr-*.md")

	if err != nil {
		return "", nil, fmt.Errorf("create pull request body file: %w", err)
	}

	path := file.Name()

	cleanup := func() {
		_ = os.Remove(path)
	}

	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()

		return "", nil, fmt.Errorf("set pull request body permissions: %w", err)
	}

	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		cleanup()

		return "", nil, fmt.Errorf("write pull request body: %w", err)
	}

	if err := file.Close(); err != nil {
		cleanup()

		return "", nil, fmt.Errorf("close pull request body: %w", err)
	}

	return path, cleanup, nil
}

func runGH(ctx context.Context, workingDirectory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "gh", args...)

	command.Dir = workingDirectory

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())

		if message == "" {
			message = err.Error()
		}

		return "", fmt.Errorf("gh %s: %s", strings.Join(args, " "), message)
	}

	return stdout.String(), nil
}

func lastNonEmptyLine(output string) string {
	lines := strings.Split(output, "\n")

	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])

		if line != "" {
			return line
		}
	}

	return ""
}

func validatePullRequestURL(value string) error {
	parsed, err := url.Parse(value)

	if err != nil {
		return fmt.Errorf("parse pull request URL %q: %w", value, err)
	}

	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("GitHub CLI returned an invalid pull request URL: %q", value)
	}

	if parsed.Host == "" {
		return fmt.Errorf("GitHub CLI returned a pull request URL without a host: %q", value)
	}

	return nil
}

func BuildPullRequestBody(preparation Preparation) string {
	var body strings.Builder

	body.WriteString("## Terraform provider upgrade\n\n")

	body.WriteString("### Providers\n\n")
	body.WriteString("| Provider | Previous | Target | Change |\n")
	body.WriteString("|---|---:|---:|---|\n")

	for _, provider := range preparation.Providers {
		fmt.Fprintf(
			&body,
			"| `%s` | `%s` | `%s` | %s |\n",
			provider.Source,
			provider.PreviousVersion,
			provider.TargetVersion,
			provider.ChangeType,
		)
	}

	body.WriteString("\n### Validation\n\n")

	fmt.Fprintf(&body, "- Recommendation: **%s**\n", preparation.Validation.Recommendation)

	fmt.Fprintf(&body, "- State context: `%s`\n", preparation.Validation.StateContext)

	fmt.Fprintf(
		&body,
		"- Risk: **%s**\n",
		preparation.Validation.Risk,
	)

	body.WriteString("\n### Plan\n\n")

	fmt.Fprintf(&body, "- Create: %d\n", preparation.Plan.Create)

	fmt.Fprintf(&body, "- Update: %d\n", preparation.Plan.Update)

	fmt.Fprintf(&body, "- Replace: %d\n", preparation.Plan.Replace)

	fmt.Fprintf(&body, "- Delete: %d\n", preparation.Plan.Delete)

	body.WriteString("\n### Differences\n\n")

	if len(preparation.AttributeDifferences) == 0 {
		body.WriteString("No plan differences detected.\n")
	} else {
		for _, difference := range preparation.AttributeDifferences {
			fmt.Fprintf(
				&body,
				"- `%s`: `%s.%s` %s",
				difference.Address,
				difference.Phase,
				difference.Path,
				difference.Kind,
			)

			if difference.SchemaOnly {
				body.WriteString(" (schema-only)")
			}

			body.WriteString("\n")
		}
	}

	body.WriteString("\n### Applied migrations\n\n")

	if len(preparation.Migrations) == 0 {
		body.WriteString("No Terraform configuration migrations were required.\n")
	} else {
		for _, migration := range preparation.Migrations {
			fmt.Fprintf(
				&body,
				"- `%s` in `%s`: %s\n",
				migration.RuleID,
				migration.RelativePath,
				migration.Description,
			)
		}
	}

	body.WriteString("\n---\n")
	body.WriteString("Generated and validated by InfraUpgrade.\n")

	return body.String()
}
