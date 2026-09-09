package upgrade

type ProviderTarget struct {
	Name           string
	Source         string
	CurrentVersion string
	TargetVersion  string
}

type ProviderVersionCheck struct {
	Name             string
	Source           string
	ExpectedBaseline string
	ActualBaseline   string
	ExpectedUpgrade  string
	ActualUpgrade    string
	BaselineFound    bool
	UpgradeFound     bool
}

func (check ProviderVersionCheck) Passed() bool {
	return check.BaselineFound && check.UpgradeFound && check.ExpectedBaseline == check.ActualBaseline && check.ExpectedUpgrade == check.ActualUpgrade
}

func VerifyProviderVersions(report Report, targets []ProviderTarget) []ProviderVersionCheck {
	checks := make([]ProviderVersionCheck, 0, len(targets))

	for _, target := range targets {
		baselineVersion, baselineFound := report.Baseline.SelectedVersions[target.Source]
		upgradedVersion, upgradeFound := report.Upgraded.SelectedVersions[target.Source]

		checks = append(checks, ProviderVersionCheck{
			Name:             target.Name,
			Source:           target.Source,
			ExpectedBaseline: target.CurrentVersion,
			ActualBaseline:   baselineVersion,
			ExpectedUpgrade:  target.TargetVersion,
			ActualUpgrade:    upgradedVersion,
			BaselineFound:    baselineFound,
			UpgradeFound:     upgradeFound,
		})
	}

	return checks
}

func AllVersionsCheckPassed(checks []ProviderVersionCheck) bool {
	if len(checks) == 0 {
		return false
	}

	for _, check := range checks {
		if !check.Passed() {
			return false
		}
	}

	return true
}
