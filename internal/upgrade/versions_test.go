package upgrade

import "testing"

func TestVerifyProviderVersions(t *testing.T) {
	report := Report{
		Baseline: ExecutionReport{SelectedVersions: map[string]string{
			"hashicorp/aws": "6.40.0",
		}},
		Upgraded: ExecutionReport{SelectedVersions: map[string]string{
			"hashicorp/aws": "6.64.0",
		}},
	}
	targets := []ProviderTarget{{
		Name:           "aws",
		Source:         "hashicorp/aws",
		CurrentVersion: "6.40.0",
		TargetVersion:  "6.64.0",
	}}

	checks := VerifyProviderVersions(report, targets)
	if len(checks) != 1 {
		t.Fatalf("len(checks) = %d, want 1", len(checks))
	}
	check := checks[0]
	if !check.Passed() || !check.BaselineFound || !check.UpgradeFound {
		t.Fatalf("check = %#v, want passed", check)
	}
	if !AllVersionsCheckPassed(checks) {
		t.Fatal("AllVersionsCheckPassed() = false, want true")
	}
}

func TestProviderVersionCheckFailsForWrongOrMissingVersion(t *testing.T) {
	tests := []struct {
		name   string
		check  ProviderVersionCheck
		passed bool
	}{
		{
			name: "wrong upgraded version",
			check: ProviderVersionCheck{
				ExpectedBaseline: "6.40.0", ActualBaseline: "6.40.0",
				ExpectedUpgrade: "6.64.0", ActualUpgrade: "6.63.0",
				BaselineFound: true, UpgradeFound: true,
			},
		},
		{
			name: "baseline missing",
			check: ProviderVersionCheck{
				ExpectedUpgrade: "6.64.0", ActualUpgrade: "6.64.0",
				UpgradeFound: true,
			},
		},
		{
			name: "both match",
			check: ProviderVersionCheck{
				ExpectedBaseline: "6.40.0", ActualBaseline: "6.40.0",
				ExpectedUpgrade: "6.64.0", ActualUpgrade: "6.64.0",
				BaselineFound: true, UpgradeFound: true,
			},
			passed: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.check.Passed(); got != test.passed {
				t.Fatalf("Passed() = %t, want %t", got, test.passed)
			}
		})
	}
}

func TestAllVersionsCheckPassedRejectsEmptyOrFailedChecks(t *testing.T) {
	if AllVersionsCheckPassed(nil) {
		t.Fatal("AllVersionsCheckPassed(nil) = true, want false")
	}

	checks := []ProviderVersionCheck{{
		ExpectedBaseline: "1.0.0",
		ActualBaseline:   "1.0.0",
		ExpectedUpgrade:  "2.0.0",
		ActualUpgrade:    "1.0.0",
		BaselineFound:    true,
		UpgradeFound:     true,
	}}
	if AllVersionsCheckPassed(checks) {
		t.Fatal("AllVersionsCheckPassed(failed check) = true, want false")
	}
}

func TestVerifyProviderVersionsReportsMissingProvider(t *testing.T) {
	checks := VerifyProviderVersions(Report{}, []ProviderTarget{{
		Name:           "aws",
		Source:         "hashicorp/aws",
		CurrentVersion: "6.40.0",
		TargetVersion:  "6.64.0",
	}})

	if len(checks) != 1 || checks[0].BaselineFound || checks[0].UpgradeFound || checks[0].Passed() {
		t.Fatalf("checks = %#v, want missing provider", checks)
	}
}
