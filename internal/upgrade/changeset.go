package upgrade

import (
	"bytes"
	"fmt"
)

type FileChange struct {
	RelativePath string
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

	changeSet := ChangeSet{
		Files: []FileChange{
			{
				RelativePath: ".terraform.lock.hcl",
				Content:      append([]byte(nil), report.Upgraded.LockFileContent...),
			},
		},
	}

	if err := validateChangeSet(changeSet); err != nil {
		return ChangeSet{}, err
	}

	return changeSet, nil
}

func validateChangeSet(changeSet ChangeSet) error {
	if len(changeSet.Files) == 0 {
		return fmt.Errorf("changeset contains no files")
	}

	for _, file := range changeSet.Files {
		if file.RelativePath != ".terraform.lock.hcl" {
			return fmt.Errorf("file is not allowed in upgrade changeset: %s", file.RelativePath)
		}

		if len(bytes.TrimSpace(file.Content)) == 0 {
			return fmt.Errorf("changeset file is empty: %s", file.RelativePath)
		}
	}

	return nil
}
