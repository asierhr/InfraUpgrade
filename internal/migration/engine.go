package migration

import (
	"fmt"
	"sort"
)

type Context struct {
	Provider       string
	CurrentVersion string
	TargetVersion  string
	ProjectRoot    string
}

type FileChange struct {
	RelativePath string
	RuleID       string
	Description  string
	Content      []byte
}

type Rule interface {
	ID() string
	Applies(context Context) (bool, error)
	Apply(context Context) ([]FileChange, error)
}

type Engine struct {
	Rules []Rule
}

func (engine Engine) Apply(context Context) ([]FileChange, error) {
	var changes []FileChange

	modifiedFiles := make(map[string]string)

	for _, rule := range engine.Rules {
		applies, err := rule.Applies(context)

		if err != nil {
			return nil, fmt.Errorf("evaluate migration rule %s: %w", rule.ID(), err)
		}

		if !applies {
			continue
		}

		ruleChanges, err := rule.Apply(context)

		if err != nil {
			return nil, fmt.Errorf("apply migration rule %s: %w", rule.ID(), err)
		}

		for _, change := range ruleChanges {
			previousRule, alreadyModified := modifiedFiles[change.RelativePath]

			if alreadyModified {
				return nil, fmt.Errorf("file %s is modified by rules %s and %s", change.RelativePath, previousRule, rule.ID())
			}

			modifiedFiles[change.RelativePath] = rule.ID()

			changes = append(changes, change)
		}
	}

	sort.Slice(changes, func(i, j int) bool {
		return changes[i].RelativePath < changes[j].RelativePath
	})

	return changes, nil
}
