package gitprepare

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asierhr/infraupgrade/internal/upgrade"
)

type Result struct {
	RepositoryRoot string
	Branch         string
	Commit         string
	ChangedFiles   []string
}

func Prepare(ctx context.Context, projectRoot string, changeSet upgrade.ChangeSet, branch string, commitMessage string) (Result, error) {

	if err := upgrade.ValidateChangeSet(changeSet); err != nil {
		return Result{}, fmt.Errorf("validate changeset: %w", err)
	}

	absoluteProject, err := filepath.Abs(projectRoot)

	if err != nil {
		return Result{}, fmt.Errorf("resolve project path: %w", err)
	}

	repositoryRootOutput, err := runGit(ctx, absoluteProject, "rev-parse", "--show-toplevel")

	if err != nil {
		return Result{}, fmt.Errorf("find git repository: %w", err)
	}

	repositoryRoot := filepath.Clean(strings.TrimSpace(repositoryRootOutput))

	relativeProject, err := filepath.Rel(repositoryRoot, absoluteProject)

	if err != nil {
		return Result{}, fmt.Errorf("resolve terraform directory in repository: %w", err)
	}

	if relativeProject == ".." || strings.HasPrefix(relativeProject, ".."+string(filepath.Separator)) {
		return Result{}, fmt.Errorf("Terraform project outside the git repository")
	}

	if err := requireCleanRepository(ctx, repositoryRoot); err != nil {
		return Result{}, err
	}

	if _, err = runGit(ctx, repositoryRoot, "check-ref-format", "--branch", branch); err != nil {
		return Result{}, fmt.Errorf("invalid branch name %q: %w", branch, err)
	}

	temporaryRoot, err := os.MkdirTemp("", "infraupgrade-prepare-")

	if err != nil {
		return Result{}, fmt.Errorf("create prepare directory: %w", err)
	}

	worktree := filepath.Join(temporaryRoot, "worktree")

	worktreeCreated := false
	commitCreated := false

	defer func() {
		if worktreeCreated {
			_, _ = runGit(context.Background(), repositoryRoot, "worktree", "remove", "--force", worktree)
		}

		if !commitCreated && worktreeCreated {
			_, _ = runGit(context.Background(), repositoryRoot, "branch", "-D", branch)
		}

		_ = os.RemoveAll(temporaryRoot)
	}()

	if _, err := runGit(ctx, repositoryRoot, "worktree", "add", "-b", branch, worktree, "HEAD"); err != nil {
		return Result{}, fmt.Errorf("create git worktree: %w", err)
	}

	worktreeCreated = true

	expectedFiles, err := applyChangeSet(worktree, relativeProject, changeSet)

	if err != nil {
		return Result{}, err
	}

	changedFiles, err := readChangedFiles(ctx, worktree)

	if err != nil {
		return Result{}, err
	}

	if err := verifyChangedFiles(expectedFiles, changedFiles); err != nil {
		return Result{}, err
	}

	for _, file := range changedFiles {
		if _, err := runGit(ctx, worktree, "add", "--", file); err != nil {
			return Result{}, fmt.Errorf("stage %s: %w", file, err)
		}
	}

	if _, err := runGit(ctx, worktree, "commit", "-m", commitMessage); err != nil {
		return Result{}, fmt.Errorf("create local commit: %w", err)
	}

	commitCreated = true

	commitOutput, err := runGit(ctx, worktree, "rev-parse", "HEAD")

	if err != nil {
		return Result{}, fmt.Errorf("read prepared commit: %w", err)
	}

	return Result{
		RepositoryRoot: repositoryRoot,
		Branch:         branch,
		Commit:         strings.TrimSpace(commitOutput),
		ChangedFiles:   changedFiles,
	}, nil
}

func requireCleanRepository(ctx context.Context, repositoryRoot string) error {
	status, err := runGit(ctx, repositoryRoot, "status", "--porcelain=v1", "--untracked-files=all")

	if err != nil {
		return fmt.Errorf("read git status: %w", err)
	}

	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("repository has uncommited or untracked files")
	}

	return nil
}

func applyChangeSet(worktree string, relativeProject string, changeSet upgrade.ChangeSet) ([]string, error) {
	expectedFiles := make([]string, 0, len(changeSet.Files))

	for _, file := range changeSet.Files {

		repositoryRelativePath := filepath.Join(relativeProject, file.RelativePath)

		targetPath := filepath.Join(worktree, repositoryRelativePath)

		if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
			return nil, fmt.Errorf("create destination directory: %w", err)
		}

		if err := os.WriteFile(targetPath, file.Content, 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", file.RelativePath, err)
		}

		expectedFiles = append(expectedFiles, filepath.ToSlash(repositoryRelativePath))
	}

	sort.Strings(expectedFiles)

	return expectedFiles, nil
}

func readChangedFiles(ctx context.Context, worktree string) ([]string, error) {
	output, err := runGit(ctx, worktree, "status", "--porcelain=v1", "-z", "--untracked-files=all")

	if err != nil {
		return nil, fmt.Errorf("read worktree changes: %w", err)
	}

	entries := strings.Split(output, "\x00")
	files := make([]string, 0)

	for _, entry := range entries {
		if entry == "" {
			continue
		}

		if len(entry) < 4 {
			return nil, fmt.Errorf("unexpected git status entry: %q", entry)
		}

		status := entry[2:]

		if strings.ContainsAny(status, "RC") {
			return nil, fmt.Errorf("renamed or copied files are not allowed")
		}

		path := filepath.ToSlash(entry[3:])

		files = append(files, path)
	}

	sort.Strings(files)

	return files, nil
}

func verifyChangedFiles(expected []string, actual []string) error {
	if len(expected) != len(actual) {
		return fmt.Errorf("unexpected changed files: expected %v, got %v", expected, actual)
	}

	for index := range expected {
		if expected[index] != actual[index] {
			return fmt.Errorf("unexpected changed files: expected %v, got %v", expected, actual)
		}
	}

	return nil
}

func runGit(ctx context.Context, workingDirectory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)

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

		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}

	return stdout.String(), nil
}
