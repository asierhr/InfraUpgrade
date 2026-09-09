package analyzer

import (
	"fmt"

	"github.com/asierhr/infraupgrade/internal/scanner"
)

type Finding struct {
	Code       string
	Severity   string
	Message    string
	Suggestion string
	Provider   string
}

func Analyze(result scanner.Result) []Finding {
	var findings []Finding

	findings = append(findings, checkTerraformVersion(result)...)
	findings = append(findings, checkProviderDeclarations(result)...)
	findings = append(findings, checkProviderConstraints(result)...)
	findings = append(findings, checkProviderLocks(result)...)

	return findings
}

func checkTerraformVersion(result scanner.Result) []Finding {
	if result.RequiredVersion != "" {
		return nil
	}

	return []Finding{
		{
			Code:       "TF001",
			Severity:   "warning",
			Message:    "Terraform version is not constrained",
			Suggestion: "Add required_version to the terraform block",
		},
	}
}

func checkProviderDeclarations(result scanner.Result) []Finding {
	var findings []Finding

	for _, provider := range result.Providers {
		if (provider.Configured || provider.Inferred) && !provider.Declared {
			findings = append(findings, Finding{
				Code:       "TF002",
				Severity:   "warning",
				Provider:   provider.Name,
				Message:    fmt.Sprintf(`Provider %q is used but not declared`, provider.Name),
				Suggestion: "Add it to required_providers",
			})
		}
	}

	return findings
}

func checkProviderConstraints(result scanner.Result) []Finding {
	var findings []Finding

	for _, provider := range result.Providers {
		if provider.Declared && provider.Constraint == "" {
			findings = append(findings, Finding{
				Code:       "TF003",
				Severity:   "warning",
				Provider:   provider.Name,
				Message:    fmt.Sprintf(`Provider %q has no version constraint`, provider.Name),
				Suggestion: fmt.Sprintf(`Add a version constraint for %q in required_providers`, provider.Name),
			})
		}
	}

	return findings
}

func checkProviderLocks(result scanner.Result) []Finding {
	var findings []Finding

	for _, provider := range result.Providers {
		if provider.Declared && provider.LockedVersion == "" {
			findings = append(findings, Finding{
				Code:       "TF004",
				Severity:   "warning",
				Provider:   provider.Name,
				Message:    fmt.Sprintf(`Provider %q is not present in the lock file`, provider.Name),
				Suggestion: "Run terraform init and commit .terraform.lock.hcl",
			})
		}
	}

	return findings
}
