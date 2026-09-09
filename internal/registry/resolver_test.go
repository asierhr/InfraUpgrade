package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	version "github.com/hashicorp/go-version"

	"github.com/asierhr/infraupgrade/internal/scanner"
)

type fakeVersionLister struct {
	versions map[string][]string
	err      error
	calls    int
}

func (fake *fakeVersionLister) ListVersions(_ context.Context, source string) ([]string, error) {
	fake.calls++
	if fake.err != nil {
		return nil, fake.err
	}
	return fake.versions[source], nil
}

func TestFindUpdatesRespectsConstraintAndIgnoresPrereleases(t *testing.T) {
	result := scanner.Result{Providers: []scanner.Provider{{
		Name:          "aws",
		Source:        "hashicorp/aws",
		Constraint:    "~> 5.0",
		LockedVersion: "5.1.0",
	}}}
	lister := &fakeVersionLister{versions: map[string][]string{
		"hashicorp/aws": {"invalid", "5.1.0", "5.2.1", "5.3.0-beta.1", "6.0.0"},
	}}

	candidates, err := FindUpdates(context.Background(), result, lister)
	if err != nil {
		t.Fatalf("FindUpdates() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}

	candidate := candidates[0]
	if candidate.LatestVersion != "6.0.0" {
		t.Errorf("LatestVersion = %q, want 6.0.0", candidate.LatestVersion)
	}
	if candidate.LatestAllowedVersion != "5.2.1" {
		t.Errorf("LatestAllowedVersion = %q, want 5.2.1", candidate.LatestAllowedVersion)
	}
	if candidate.TargetVersion != "5.2.1" {
		t.Errorf("TargetVersion = %q, want 5.2.1", candidate.TargetVersion)
	}
	if candidate.ChangeType != "minor" {
		t.Errorf("ChangeType = %q, want minor", candidate.ChangeType)
	}
	if !candidate.UpdateAvailable {
		t.Error("UpdateAvailable = false, want true")
	}
}

func TestFindUpdatesUsesLatestWithoutConstraint(t *testing.T) {
	result := scanner.Result{Providers: []scanner.Provider{{
		Name:          "aws",
		Source:        "hashicorp/aws",
		LockedVersion: "5.1.0",
	}}}
	lister := &fakeVersionLister{versions: map[string][]string{
		"hashicorp/aws": {"5.1.0", "6.0.0"},
	}}

	candidates, err := FindUpdates(context.Background(), result, lister)
	if err != nil {
		t.Fatalf("FindUpdates() error = %v", err)
	}

	candidate := candidates[0]
	if candidate.TargetVersion != "6.0.0" || candidate.ChangeType != "major" {
		t.Fatalf("target = %q (%s), want 6.0.0 (major)", candidate.TargetVersion, candidate.ChangeType)
	}
}

func TestFindUpdatesReportsNoUpdate(t *testing.T) {
	result := scanner.Result{Providers: []scanner.Provider{{
		Name:          "aws",
		Source:        "hashicorp/aws",
		LockedVersion: "6.0.0",
	}}}
	lister := &fakeVersionLister{versions: map[string][]string{
		"hashicorp/aws": {"5.0.0", "6.0.0"},
	}}

	candidates, err := FindUpdates(context.Background(), result, lister)
	if err != nil {
		t.Fatalf("FindUpdates() error = %v", err)
	}

	candidate := candidates[0]
	if candidate.UpdateAvailable || candidate.TargetVersion != "6.0.0" || candidate.ChangeType != "none" {
		t.Fatalf("candidate = %#v, want no update", candidate)
	}
}

func TestFindUpdatesSkipsIncompleteProviders(t *testing.T) {
	result := scanner.Result{Providers: []scanner.Provider{
		{Name: "aws", LockedVersion: "6.0.0"},
		{Name: "random", Source: "hashicorp/random"},
	}}
	lister := &fakeVersionLister{}

	candidates, err := FindUpdates(context.Background(), result, lister)
	if err != nil {
		t.Fatalf("FindUpdates() error = %v", err)
	}
	if len(candidates) != 0 || lister.calls != 0 {
		t.Fatalf("candidates = %#v, registry calls = %d", candidates, lister.calls)
	}
}

func TestFindUpdatesReportsRegistryError(t *testing.T) {
	result := scanner.Result{Providers: []scanner.Provider{{
		Name:          "aws",
		Source:        "hashicorp/aws",
		LockedVersion: "6.0.0",
	}}}
	lister := &fakeVersionLister{err: errors.New("offline")}

	_, err := FindUpdates(context.Background(), result, lister)
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("FindUpdates() error = %v, want registry error", err)
	}
}

func TestResolveProviderErrors(t *testing.T) {
	tests := []struct {
		name      string
		provider  scanner.Provider
		available []string
		wantError string
	}{
		{
			name:      "invalid locked version",
			provider:  scanner.Provider{LockedVersion: "invalid"},
			available: []string{"6.0.0"},
			wantError: "invalid locked version",
		},
		{
			name:      "invalid constraint",
			provider:  scanner.Provider{LockedVersion: "5.0.0", Constraint: "invalid"},
			available: []string{"6.0.0"},
			wantError: "invalid constraint",
		},
		{
			name:      "no stable versions",
			provider:  scanner.Provider{LockedVersion: "5.0.0"},
			available: []string{"invalid", "6.0.0-beta.1"},
			wantError: "no stable version",
		},
		{
			name:      "no allowed versions",
			provider:  scanner.Provider{LockedVersion: "5.0.0", Constraint: "~> 5.0"},
			available: []string{"6.0.0"},
			wantError: "no version satisfies constraint",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveProvider(test.provider, test.available)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("resolveProvider() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestChangeType(t *testing.T) {
	tests := []struct {
		current string
		target  string
		want    string
	}{
		{current: "1.0.0", target: "2.0.0", want: "major"},
		{current: "1.0.0", target: "1.1.0", want: "minor"},
		{current: "1.0.0", target: "1.0.1", want: "patch"},
		{current: "1.0.0", target: "1.0.0", want: "none"},
		{current: "2.0.0", target: "1.0.0", want: "none"},
	}

	for _, test := range tests {
		t.Run(test.current+"_to_"+test.target, func(t *testing.T) {
			current := version.Must(version.NewVersion(test.current))
			target := version.Must(version.NewVersion(test.target))

			if got := changeType(current, target); got != test.want {
				t.Fatalf("changeType() = %q, want %q", got, test.want)
			}
		})
	}
}
