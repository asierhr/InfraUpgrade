package upgrade

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandRunnerCapturesSuccessfulCommand(t *testing.T) {
	workingDirectory := t.TempDir()
	runner := CommandRunner{Executable: os.Args[0]}

	result, err := runner.Run(
		context.Background(),
		workingDirectory,
		"-test.run=TestCommandRunnerHelperProcess",
		"--",
		"success",
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "helper stdout") {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(result.Stdout, filepath.Clean(workingDirectory)) {
		t.Fatalf("stdout = %q, want working directory", result.Stdout)
	}
}

func TestCommandRunnerCapturesExitError(t *testing.T) {
	runner := CommandRunner{Executable: os.Args[0]}

	result, err := runner.Run(
		context.Background(),
		t.TempDir(),
		"-test.run=TestCommandRunnerHelperProcess",
		"--",
		"failure",
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitCode != 7 || !strings.Contains(result.Stderr, "helper stderr") {
		t.Fatalf("result = %#v", result)
	}
}

func TestCommandRunnerReportsMissingExecutable(t *testing.T) {
	runner := CommandRunner{Executable: filepath.Join(t.TempDir(), "missing-command")}

	_, err := runner.Run(context.Background(), t.TempDir(), "version")
	if err == nil || !strings.Contains(err.Error(), "start terraform") {
		t.Fatalf("Run() error = %v, want start error", err)
	}
}

func TestCommandRunnerHelperProcess(t *testing.T) {
	mode := ""
	for index, argument := range os.Args {
		if argument == "--" && index+1 < len(os.Args) {
			mode = os.Args[index+1]
			break
		}
	}

	if mode == "" {
		return
	}

	workingDirectory, _ := os.Getwd()
	if mode == "success" {
		fmt.Printf("helper stdout: %s", workingDirectory)
		os.Exit(0)
	}

	fmt.Fprint(os.Stderr, "helper stderr")
	os.Exit(7)
}
