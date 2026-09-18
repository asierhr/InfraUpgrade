package gitprepare

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("INFRAUPGRADE_FAKE_GH") == "1" {
		fakeGHMain()
		return
	}

	os.Exit(m.Run())
}

func TestBuildPullRequestBodyIncludesPreparedResults(t *testing.T) {
	preparation := Preparation{
		Providers: []PreparedProvider{{
			Source: "hashicorp/aws", PreviousVersion: "6.40.0", TargetVersion: "6.64.0", ChangeType: "minor",
		}},
		Validation: PreparedValidation{
			Recommendation: "safe", StateContext: "existing-infrastructure", Risk: "low",
		},
		Plan: PreparedPlan{Create: 1, Update: 2, Replace: 3, Delete: 4},
		AttributeDifferences: []PreparedAttributeDifference{{
			Address: "aws_route.example", Phase: "after", Path: "new_field", Kind: "added", SchemaOnly: true,
		}},
		Migrations: []PreparedMigration{{
			RuleID: "schema-derived", RelativePath: "main.tf", Description: "rename field",
		}},
	}

	body := BuildPullRequestBody(preparation)
	for _, expected := range []string{
		"## Terraform provider upgrade",
		"`hashicorp/aws` | `6.40.0` | `6.64.0` | minor",
		"Recommendation: **safe**",
		"State context: `existing-infrastructure`",
		"Risk: **low**",
		"Create: 1",
		"Update: 2",
		"Replace: 3",
		"Delete: 4",
		"`aws_route.example`: `after.new_field` added (schema-only)",
		"`schema-derived` in `main.tf`: rename field",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("BuildPullRequestBody() does not contain %q:\n%s", expected, body)
		}
	}
}

func TestBuildPullRequestBodyHandlesEmptyDifferencesAndMigrations(t *testing.T) {
	body := BuildPullRequestBody(Preparation{})
	if !strings.Contains(body, "No plan differences detected.") {
		t.Fatalf("body does not report empty differences:\n%s", body)
	}
	if !strings.Contains(body, "No Terraform configuration migrations were required.") {
		t.Fatalf("body does not report empty migrations:\n%s", body)
	}
}

func TestPublishPullRequestPushesPreparedCommitAndCreatesPR(t *testing.T) {
	repository, project, prepared, preparationPath := createPublishFixture(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitTest(t, repository, "init", "--bare", remote)
	gitTest(t, repository, "remote", "add", "origin", remote)

	logPath, bodyPath := installFakeGH(t, "", "https://github.com/example/repository/pull/42")

	result, err := PublishPullRequest(
		context.Background(),
		project,
		prepared.Branch,
		PublishOptions{},
	)
	if err != nil {
		t.Fatalf("PublishPullRequest() error = %v", err)
	}
	if result.URL != "https://github.com/example/repository/pull/42" || result.AlreadyExisted {
		t.Fatalf("PublishPullRequest() = %#v", result)
	}
	if result.Branch != prepared.Branch || result.BaseBranch != "main" || result.Remote != "origin" || result.Commit != prepared.Commit {
		t.Fatalf("PublishPullRequest() identity = %#v", result)
	}
	if result.PreparationPath != preparationPath {
		t.Fatalf("PublishPullRequest() preparation = %q; want %q", result.PreparationPath, preparationPath)
	}

	remoteCommit := strings.TrimSpace(gitTest(t, repository, "--git-dir", remote, "rev-parse", "refs/heads/"+prepared.Branch))
	if remoteCommit != prepared.Commit {
		t.Fatalf("remote commit = %q; want %q", remoteCommit, prepared.Commit)
	}

	logContent, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logContent)
	for _, expected := range []string{`["auth","status"]`, `["pr","view"`, `["pr","create"`, "--head", prepared.Branch, "--base", "main"} {
		if !strings.Contains(log, expected) {
			t.Fatalf("fake gh log does not contain %q:\n%s", expected, log)
		}
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatalf("read captured PR body: %v", err)
	}
	if !strings.Contains(string(body), "hashicorp/aws") || !strings.Contains(string(body), "Create: 1") {
		t.Fatalf("captured PR body is incomplete:\n%s", body)
	}
}

func TestPublishPullRequestReturnsExistingPRWithoutCreatingAnother(t *testing.T) {
	repository, project, prepared, _ := createPublishFixture(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitTest(t, repository, "init", "--bare", remote)
	gitTest(t, repository, "remote", "add", "upstream", remote)

	logPath, _ := installFakeGH(t, "https://github.com/example/repository/pull/7", "")

	result, err := PublishPullRequest(
		context.Background(),
		project,
		prepared.Branch,
		PublishOptions{Remote: "upstream"},
	)
	if err != nil {
		t.Fatalf("PublishPullRequest() error = %v", err)
	}
	if !result.AlreadyExisted || result.URL != "https://github.com/example/repository/pull/7" || result.Remote != "upstream" {
		t.Fatalf("PublishPullRequest() = %#v", result)
	}
	logContent, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logContent), "pr create") {
		t.Fatalf("existing PR was created again:\n%s", logContent)
	}
}

func TestPublishPullRequestRejectsWrongTerraformDirectoryBeforePush(t *testing.T) {
	repository, _, prepared, _ := createPublishFixture(t)

	_, err := PublishPullRequest(
		context.Background(),
		repository,
		prepared.Branch,
		PublishOptions{},
	)
	if err == nil || !strings.Contains(err.Error(), "belongs to Terraform directory") {
		t.Fatalf("PublishPullRequest() error = %v; want Terraform directory error", err)
	}
}

func TestPullRequestHelpers(t *testing.T) {
	if got := lastNonEmptyLine("first\n\nhttps://example.test/pull/1\n"); got != "https://example.test/pull/1" {
		t.Fatalf("lastNonEmptyLine() = %q", got)
	}
	for _, invalid := range []string{"", "not-a-url", "file:///tmp/pr"} {
		if err := validatePullRequestURL(invalid); err == nil {
			t.Fatalf("validatePullRequestURL(%q) error = nil", invalid)
		}
	}
	if err := validatePullRequestURL("https://github.com/example/repository/pull/1"); err != nil {
		t.Fatalf("validatePullRequestURL(valid) error = %v", err)
	}

	path, cleanup, err := writePullRequestBody("body content")
	if err != nil {
		t.Fatalf("writePullRequestBody() error = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "body content" {
		t.Fatalf("body file = %q, %v", content, err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("body file still exists after cleanup: %v", err)
	}
}

func createPublishFixture(t *testing.T) (string, string, Result, string) {
	t.Helper()

	repository, project, prepared := createPreparedUpgrade(t)
	input := completePreparationInput()
	input.ActionDifferences = nil
	input.Validation.ActionDifferences = 0
	input.Validation.Risk = "low"
	input.Validation.Recommendation = "safe"
	path, err := SavePreparation(context.Background(), prepared, input)
	if err != nil {
		t.Fatalf("SavePreparation() error = %v", err)
	}

	return repository, project, prepared, path
}

func installFakeGH(t *testing.T, existingURL string, createURL string) (string, string) {
	t.Helper()

	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "gh"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	fakePath := filepath.Join(directory, name)
	copyExecutable(t, executable, fakePath)

	logPath := filepath.Join(directory, "gh.log")
	bodyPath := filepath.Join(directory, "body.md")
	t.Setenv("INFRAUPGRADE_FAKE_GH", "1")
	t.Setenv("INFRAUPGRADE_FAKE_GH_LOG", logPath)
	t.Setenv("INFRAUPGRADE_FAKE_GH_BODY", bodyPath)
	t.Setenv("INFRAUPGRADE_FAKE_GH_EXISTING_URL", existingURL)
	t.Setenv("INFRAUPGRADE_FAKE_GH_CREATE_URL", createURL)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))

	return logPath, bodyPath
}

func copyExecutable(t *testing.T, source string, destination string) {
	t.Helper()

	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func fakeGHMain() {
	arguments := os.Args[1:]
	if logPath := os.Getenv("INFRAUPGRADE_FAKE_GH_LOG"); logPath != "" {
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			encoded, _ := json.Marshal(arguments)
			_, _ = fmt.Fprintln(file, string(encoded))
			_ = file.Close()
		}
	}

	if len(arguments) >= 2 && arguments[0] == "auth" && arguments[1] == "status" {
		os.Exit(0)
	}
	if len(arguments) >= 2 && arguments[0] == "pr" && arguments[1] == "view" {
		if value := os.Getenv("INFRAUPGRADE_FAKE_GH_EXISTING_URL"); value != "" {
			fmt.Println(value)
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "no pull requests found")
		os.Exit(1)
	}
	if len(arguments) >= 2 && arguments[0] == "pr" && arguments[1] == "create" {
		for index, argument := range arguments {
			if argument != "--body-file" || index+1 >= len(arguments) {
				continue
			}
			content, err := os.ReadFile(arguments[index+1])
			if err == nil {
				_ = os.WriteFile(os.Getenv("INFRAUPGRADE_FAKE_GH_BODY"), content, 0o600)
			}
		}
		fmt.Println(os.Getenv("INFRAUPGRADE_FAKE_GH_CREATE_URL"))
		os.Exit(0)
	}

	fmt.Fprintln(os.Stderr, "unexpected fake gh arguments")
	os.Exit(2)
}
