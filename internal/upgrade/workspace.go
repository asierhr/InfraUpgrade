package upgrade

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

var skippedDirection = map[string]bool{
	".git":              true,
	".terraform":        true,
	".terragrunt-cache": true,
	".idea":             true,
	".vscode":           true,
	"node_modules":      true,
}

func copyProject(source string) (string, func() error, error) {
	absoluteSource, err := filepath.Abs(source)

	if err != nil {
		return "", nil, fmt.Errorf("resolve project path: %w", err)
	}

	info, err := os.Stat(absoluteSource)

	if err != nil {
		return "", nil, fmt.Errorf("read project path: %w", err)
	}

	if !info.IsDir() {
		return "", nil, fmt.Errorf("project path is not a directory: %s", absoluteSource)
	}

	temporaryRoot, err := os.MkdirTemp("", "infraupgrade-")

	if err != nil {
		return "", nil, fmt.Errorf("create temporary workspace: %w", err)
	}

	cleanup := func() error {
		return os.RemoveAll(temporaryRoot)
	}

	destination := filepath.Join(temporaryRoot, "project")

	if err := os.Mkdir(destination, 0o700); err != nil {
		_ = cleanup()

		return "", nil, fmt.Errorf("create project workspace: %w", err)
	}

	err = filepath.WalkDir(absoluteSource, func(path string, entry fs.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}

		relativePath, err := filepath.Rel(absoluteSource, path)

		if err != nil {
			return err
		}

		if relativePath == "." {
			return nil
		}

		if entry.IsDir() && skippedDirection[entry.Name()] {
			return filepath.SkipDir
		}

		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not supported: %s", relativePath)
		}

		targetPath := filepath.Join(destination, relativePath)

		if entry.IsDir() {
			return os.MkdirAll(targetPath, 0o700)
		}

		if shouldSkipFile(entry.Name()) {
			return nil
		}

		return copyFile(path, targetPath, entry)
	})

	if err != nil {
		_ = cleanup()

		return "", nil, fmt.Errorf("copy project: %w", err)
	}

	return destination, cleanup, nil
}

func shouldSkipFile(name string) bool {
	switch name {

	case "infraupgrade.plan":
		return true

	case ".terraform.tfstate.lock.info":
		return true

	case "infraupgrade.tfplan":
		return true
	default:
		return false
	}
}

func copyFile(source string, destination string, entry fs.DirEntry) error {
	input, err := os.Open(source)

	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}

	defer input.Close()

	info, err := entry.Info()

	if err != nil {
		return fmt.Errorf("read source file information: %w", err)
	}

	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())

	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}

	_, copyError := io.Copy(output, input)
	closeError := output.Close()

	if copyError != nil {
		return fmt.Errorf("copy file contents: %w", copyError)
	}

	if closeError != nil {
		return fmt.Errorf("close destination file: %w", closeError)
	}

	return nil
}
