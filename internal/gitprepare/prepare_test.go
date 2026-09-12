package gitprepare

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asierhr/infraupgrade/internal/upgrade"
)

func TestPrepareCreatesLocalBranchAndCommit(t *testing.T) {
	repository, project := createGitRepository(t)
	originalLock := []byte("old lock\n")
	writeTestFile(t, filepath.Join(project, ".terraform.lock.hcl"), originalLock)
	writeTestFile(t, filepath.Join(project, "main.tf"), []byte("resource \"null_resource\" \"example\" {}\n"))
	gitTest(t, repository, "add", ".")
	gitTest(t, repository, "commit", "-m", "initial")

	changeSet := upgrade.ChangeSet{Files: []upgrade.FileChange{
		{RelativePath: ".terraform.lock.hcl", Kind: upgrade.ChangeLockFile, Content: []byte("new lock\n")},
		{RelativePath: "versions.tf", Kind: upgrade.ChangeProviderDeclaration, Content: []byte("terraform {}\n")},
	}}
	result, err := Prepare(context.Background(), project, changeSet, "infraupgrade/aws-6.64.0", "upgrade provider")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	if result.RepositoryRoot != filepath.Clean(repository) || result.Branch != "infraupgrade/aws-6.64.0" || result.Commit == "" {
		t.Fatalf("Prepare() = %#v", result)
	}
	wantFiles := []string{"Terraform/.terraform.lock.hcl", "Terraform/versions.tf"}
	if !reflect.DeepEqual(result.ChangedFiles, wantFiles) {
		t.Fatalf("Prepare() changed files = %v; want %v", result.ChangedFiles, wantFiles)
	}

	currentLock, err := os.ReadFile(filepath.Join(project, ".terraform.lock.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(currentLock) != string(originalLock) {
		t.Fatalf("original worktree was modified: %q", currentLock)
	}
	if _, err := os.Stat(filepath.Join(project, "versions.tf")); !os.IsNotExist(err) {
		t.Fatalf("versions.tf exists in original worktree; error = %v", err)
	}

	show := gitTest(t, repository, "show", "infraupgrade/aws-6.64.0:Terraform/versions.tf")
	if show != "terraform {}\n" {
		t.Fatalf("prepared versions.tf = %q", show)
	}
	message := strings.TrimSpace(gitTest(t, repository, "log", "-1", "--format=%s", "infraupgrade/aws-6.64.0"))
	if message != "upgrade provider" {
		t.Fatalf("commit message = %q", message)
	}
}

func TestPrepareRejectsDirtyRepository(t *testing.T) {
	repository, project := createGitRepository(t)
	writeTestFile(t, filepath.Join(project, ".terraform.lock.hcl"), []byte("old\n"))
	gitTest(t, repository, "add", ".")
	gitTest(t, repository, "commit", "-m", "initial")
	writeTestFile(t, filepath.Join(repository, "untracked.txt"), []byte("dirty"))

	changeSet := upgrade.ChangeSet{Files: []upgrade.FileChange{{
		RelativePath: ".terraform.lock.hcl", Kind: upgrade.ChangeLockFile, Content: []byte("new\n"),
	}}}
	_, err := Prepare(context.Background(), project, changeSet, "infraupgrade/test", "upgrade")
	if err == nil || !strings.Contains(err.Error(), "untracked files") {
		t.Fatalf("Prepare() error = %v; want dirty repository error", err)
	}
}

func TestPrepareRejectsInvalidChangeSetBeforeRunningGit(t *testing.T) {
	_, err := Prepare(context.Background(), t.TempDir(), upgrade.ChangeSet{}, "infraupgrade/test", "upgrade")
	if err == nil || !strings.Contains(err.Error(), "validate changeset") {
		t.Fatalf("Prepare() error = %v", err)
	}
}

func TestReadChangedFilesDoesNotInterpretPathAsStatus(t *testing.T) {
	repository, _ := createGitRepository(t)
	writeTestFile(t, filepath.Join(repository, "Config.tf"), []byte("original\n"))
	gitTest(t, repository, "add", ".")
	gitTest(t, repository, "commit", "-m", "initial")
	writeTestFile(t, filepath.Join(repository, "Config.tf"), []byte("changed\n"))

	files, err := readChangedFiles(context.Background(), repository)
	if err != nil {
		t.Fatalf("readChangedFiles() error = %v", err)
	}
	if !reflect.DeepEqual(files, []string{"Config.tf"}) {
		t.Fatalf("readChangedFiles() = %v", files)
	}
}

func TestVerifyChangedFiles(t *testing.T) {
	if err := verifyChangedFiles([]string{"a.tf", "b.tf"}, []string{"a.tf", "b.tf"}); err != nil {
		t.Fatalf("verifyChangedFiles(equal) error = %v", err)
	}
	if err := verifyChangedFiles([]string{"a.tf"}, []string{"b.tf"}); err == nil {
		t.Fatal("verifyChangedFiles(different) error = nil")
	}
}

func createGitRepository(t *testing.T) (string, string) {
	t.Helper()
	repository := t.TempDir()
	gitTest(t, repository, "init", "-b", "main")
	gitTest(t, repository, "config", "user.name", "InfraUpgrade Tests")
	gitTest(t, repository, "config", "user.email", "infraupgrade@example.test")
	project := filepath.Join(repository, "Terraform")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	return repository, project
}

func writeTestFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	output, err := runGit(context.Background(), directory, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return output
}
