package migration

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeRule struct {
	id         string
	applies    bool
	appliesErr error
	changes    []FileChange
	applyErr   error
}

func (rule fakeRule) ID() string { return rule.id }
func (rule fakeRule) Applies(Context) (bool, error) {
	return rule.applies, rule.appliesErr
}
func (rule fakeRule) Apply(Context) ([]FileChange, error) { return rule.changes, rule.applyErr }

func TestEngineApplyRunsApplicableRulesAndSortsChanges(t *testing.T) {
	engine := Engine{Rules: []Rule{
		fakeRule{id: "skip", applies: false},
		fakeRule{id: "second", applies: true, changes: []FileChange{{RelativePath: "z.tf", RuleID: "second"}}},
		fakeRule{id: "first", applies: true, changes: []FileChange{{RelativePath: "a.tf", RuleID: "first"}}},
	}}

	changes, err := engine.Apply(Context{})
	if err != nil {
		t.Fatalf("Engine.Apply() error = %v", err)
	}
	paths := []string{changes[0].RelativePath, changes[1].RelativePath}
	if !reflect.DeepEqual(paths, []string{"a.tf", "z.tf"}) {
		t.Fatalf("Engine.Apply() paths = %v", paths)
	}
}

func TestEngineApplyRejectsConflictingRules(t *testing.T) {
	engine := Engine{Rules: []Rule{
		fakeRule{id: "one", applies: true, changes: []FileChange{{RelativePath: "main.tf"}}},
		fakeRule{id: "two", applies: true, changes: []FileChange{{RelativePath: "main.tf"}}},
	}}
	_, err := engine.Apply(Context{})
	if err == nil || !strings.Contains(err.Error(), "modified by rules one and two") {
		t.Fatalf("Engine.Apply() error = %v", err)
	}
}

func TestEngineApplyWrapsRuleErrors(t *testing.T) {
	tests := []struct {
		name string
		rule fakeRule
		want string
	}{
		{name: "applies", rule: fakeRule{id: "broken", appliesErr: errors.New("boom")}, want: "evaluate migration rule broken"},
		{name: "apply", rule: fakeRule{id: "broken", applies: true, applyErr: errors.New("boom")}, want: "apply migration rule broken"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := (Engine{Rules: []Rule{test.rule}}).Apply(Context{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Engine.Apply() error = %v; want containing %q", err, test.want)
			}
		})
	}
}
