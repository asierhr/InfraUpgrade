package upgrade

import (
	"context"
	"fmt"
	"sort"

	"github.com/asierhr/infraupgrade/internal/migration"
)

type AppliedMigration struct {
	RelativePath string
	RuleID       string
	Description  string
	Content      []byte
}

type workspaceMigrator func(workspace string) ([]AppliedMigration, error)

func DryRunWithMigrations(ctx context.Context, projectRoot string, runner Runner, engine migration.Engine, targets []ProviderTarget) (Report, error) {
	migrate := func(workspace string) ([]AppliedMigration, error) {
		return applyMigrations(engine, targets, workspace)
	}

	return dryRun(ctx, projectRoot, runner, migrate)
}

func applyMigrations(engine migration.Engine, targets []ProviderTarget, workspace string) ([]AppliedMigration, error) {
	var applied []AppliedMigration

	modifiedFiles := make(map[string]string)

	for _, target := range targets {
		changes, err := engine.Apply(migration.Context{
			Provider:       target.Name,
			CurrentVersion: target.CurrentVersion,
			TargetVersion:  target.TargetVersion,
			ProjectRoot:    workspace,
		})

		if err != nil {
			return nil, fmt.Errorf("apply %s migration: %w", target.Name, err)
		}

		for _, change := range changes {
			previousRule, exists := modifiedFiles[change.RelativePath]

			if exists {
				return nil, fmt.Errorf("migration rules %s and %s modify %s", previousRule, change.RuleID, change.RelativePath)
			}

			modifiedFiles[change.RelativePath] = change.RuleID

			applied = append(applied, AppliedMigration{
				RelativePath: change.RelativePath,
				RuleID:       change.RuleID,
				Description:  change.Description,
				Content:      append([]byte(nil), change.Content...),
			})
		}
	}

	sort.Slice(applied, func(i, j int) bool {
		return applied[i].RelativePath < applied[j].RelativePath
	})

	return applied, nil
}
