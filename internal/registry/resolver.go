package registry

import (
	"context"
	"fmt"

	version "github.com/hashicorp/go-version"

	"github.com/asierhr/infraupgrade/internal/scanner"
)

type UpgradeCandidate struct {
	Provider             string
	Source               string
	Constraint           string
	CurrentVersion       string
	LatestVersion        string
	LatestAllowedVersion string
	TargetVersion        string
	ChangeType           string
	UpdateAvailable      bool
}

func FindUpdates(ctx context.Context, result scanner.Result, lister VersionLister) ([]UpgradeCandidate, error) {
	var candidates []UpgradeCandidate

	for _, provider := range result.Providers {
		if provider.Source == "" || provider.LockedVersion == "" {
			continue
		}

		available, err := lister.ListVersions(ctx, provider.Source)

		if err != nil {
			return nil, fmt.Errorf("list versioins for %s: %w", provider.Name, err)
		}

		candidate, err := resolveProvider(provider, available)

		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", provider.Name, err)
		}

		candidates = append(candidates, candidate)
	}

	return candidates, nil
}

func resolveProvider(provider scanner.Provider, available []string) (UpgradeCandidate, error) {
	current, err := version.NewVersion(provider.LockedVersion)

	if err != nil {
		return UpgradeCandidate{}, fmt.Errorf("invalid locked version %q", provider.LockedVersion)
	}

	var constraint version.Constraints

	if provider.Constraint != "" {
		constraint, err = version.NewConstraint(provider.Constraint)

		if err != nil {
			return UpgradeCandidate{}, fmt.Errorf("invalid constraint %q", provider.Constraint)
		}
	}

	var latest *version.Version
	var latestAllowed *version.Version

	for _, rawVersion := range available {
		candidate, err := version.NewVersion(rawVersion)

		if err != nil || candidate.Prerelease() != "" {
			continue
		}

		if latest == nil || candidate.GreaterThan(latest) {
			latest = candidate
		}

		allowed := constraint == nil || constraint.Check(candidate)

		if allowed && (latestAllowed == nil || candidate.GreaterThan(latestAllowed)) {
			latestAllowed = candidate
		}
	}

	if latest == nil {
		return UpgradeCandidate{}, fmt.Errorf("no stable version found")
	}

	if latestAllowed == nil {
		return UpgradeCandidate{}, fmt.Errorf("no version satisfies constraint %q", provider.Constraint)
	}

	target := current

	if latestAllowed.GreaterThan(current) {
		target = latestAllowed
	}

	return UpgradeCandidate{
		Provider:             provider.Name,
		Source:               provider.Source,
		Constraint:           provider.Constraint,
		CurrentVersion:       current.String(),
		LatestVersion:        latest.String(),
		LatestAllowedVersion: latestAllowed.String(),
		TargetVersion:        target.String(),
		ChangeType:           changeType(current, target),
		UpdateAvailable:      target.GreaterThan(current),
	}, nil
}

func changeType(current *version.Version, target *version.Version) string {

	if !target.GreaterThan(current) {
		return "none"
	}

	currentParts := current.Segments64()
	targetParts := target.Segments64()

	if currentParts[0] != targetParts[0] {
		return "major"
	}

	if currentParts[1] != targetParts[1] {
		return "minor"
	}

	return "patch"
}
