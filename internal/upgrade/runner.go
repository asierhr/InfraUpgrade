package upgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

type CommandResult struct {
	Args     []string
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

type CommandRunner struct {
	Executable string
}

type Runner interface {
	Run(ctx context.Context, workingDirectory string, args ...string) (CommandResult, error)
}

func NewCommandRunner() CommandRunner {
	return CommandRunner{
		Executable: "terraform",
	}
}

func (runner CommandRunner) Run(ctx context.Context, workingDirectory string, args ...string) (CommandResult, error) {
	executable := runner.Executable

	if executable == "" {
		executable = "terraform"
	}

	command := exec.CommandContext(ctx, executable, args...)

	command.Dir = workingDirectory

	command.Env = append(os.Environ(), "TF_IN_AUTOMATION=1", "TF_INPUT=0")

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	command.Stdout = &stdout
	command.Stderr = &stderr

	started := time.Now()

	err := command.Run()

	result := CommandResult{
		Args:     append([]string(nil), args...),
		ExitCode: -1,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(started),
	}

	if err == nil {
		result.ExitCode = 0
		return result, nil
	}

	if ctx.Err() != nil {
		return result, ctx.Err()
	}

	var exitError *exec.ExitError

	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}

	return result, fmt.Errorf("start terraform: %w", err)
}
