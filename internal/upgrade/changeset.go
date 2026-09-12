package upgrade

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

type ChangeKind string

const (
	ChangeLockFile            ChangeKind = "lockfile"
	ChangeProviderDeclaration ChangeKind = "provider-declaration"
)

type FileChange struct {
	RelativePath string
	Kind         ChangeKind
	Content      []byte
}

type ChangeSet struct {
	Files []FileChange
}

func BuildLockfileChangeSet(report Report) (ChangeSet, error) {
	if !report.Upgraded.Succeeded() {
		return ChangeSet{}, fmt.Errorf("cannot build changeset from failed upgrade")
	}

	if !report.Upgraded.LockFileChanged {
		return ChangeSet{}, fmt.Errorf("upgrade lockile did not change")
	}

	if len(report.Upgraded.LockFileContent) == 0 {
		return ChangeSet{}, fmt.Errorf("upgrade lockfile is empty")
	}

	changeSet := ChangeSet{
		Files: []FileChange{
			{
				RelativePath: ".terraform.lock.hcl",
				Kind:         ChangeLockFile,
				Content:      append([]byte(nil), report.Upgraded.LockFileContent...),
			},
		},
	}

	if err := ValidateChangeSet(changeSet); err != nil {
		return ChangeSet{}, err
	}

	return changeSet, nil
}

func BuildPrepareChangeSet(report Report, projectRoot string, declarations []ProviderDeclaration) (ChangeSet, error) {
	changeSet, err := BuildLockfileChangeSet(report)

	if err != nil {
		return ChangeSet{}, err
	}

	declarationChange, err := BuildProviderDeclarationChange(projectRoot, declarations)

	if err != nil {
		return ChangeSet{}, err
	}

	if declarationChange != nil {
		changeSet.Files = append(changeSet.Files, *declarationChange)
	}

	if err := ValidateChangeSet(changeSet); err != nil {
		return ChangeSet{}, err
	}

	return changeSet, nil
}

func ValidateChangeSet(changeSet ChangeSet) error {
	if len(changeSet.Files) == 0 {
		return fmt.Errorf("changeset contains no files")
	}

	paths := make(map[string]bool)

	for _, file := range changeSet.Files {
		path := filepath.Clean(file.RelativePath)

		if filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
			return fmt.Errorf("invalid changeset path: %s", file.RelativePath)
		}

		normalizedPath := filepath.ToSlash(path)

		if paths[normalizedPath] {
			return fmt.Errorf("duplicate changeset file: %s", normalizedPath)
		}

		paths[normalizedPath] = true

		switch file.Kind {
		case ChangeLockFile:
			if normalizedPath != ".terraform.lock.hcl" {
				return fmt.Errorf("lockfile change has unexpected path: %s", normalizedPath)
			}
		case ChangeProviderDeclaration:
			if filepath.Ext(normalizedPath) != ".tf" {
				return fmt.Errorf("provider declaration must be a .tf file: %s", normalizedPath)
			}
		default:

			return fmt.Errorf("unsupported change kind %q for %s", file.Kind, normalizedPath)
		}

		if len(bytes.TrimSpace(file.Content)) == 0 {
			return fmt.Errorf("changeset file is empty: %s", normalizedPath)
		}
	}

	return nil
}
