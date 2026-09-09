package analyzer

import (
	"reflect"
	"testing"

	"github.com/asierhr/infraupgrade/internal/scanner"
)

func TestAnalyzeReturnsApplicableFindings(t *testing.T) {
	result := scanner.Result{
		Providers: []scanner.Provider{
			{Name: "aws", Configured: true, Inferred: true},
			{Name: "random", Source: "hashicorp/random", Declared: true},
		},
	}

	findings := Analyze(result)
	codes := make([]string, 0, len(findings))
	for _, finding := range findings {
		codes = append(codes, finding.Code)
	}

	want := []string{"TF001", "TF002", "TF003", "TF004"}
	if !reflect.DeepEqual(codes, want) {
		t.Fatalf("codes = %#v, want %#v", codes, want)
	}
}

func TestAnalyzeAcceptsCompleteConfiguration(t *testing.T) {
	result := scanner.Result{
		RequiredVersion: ">= 1.6.0",
		Providers: []scanner.Provider{{
			Name:          "aws",
			Source:        "hashicorp/aws",
			Constraint:    "~> 6.0",
			LockedVersion: "6.40.0",
			Declared:      true,
			Configured:    true,
			Inferred:      true,
		}},
	}

	if findings := Analyze(result); len(findings) != 0 {
		t.Fatalf("Analyze() = %#v, want no findings", findings)
	}
}

func TestUndeclaredProviderDoesNotProduceDuplicateWarnings(t *testing.T) {
	result := scanner.Result{
		RequiredVersion: ">= 1.6.0",
		Providers:       []scanner.Provider{{Name: "aws", Configured: true}},
	}

	findings := Analyze(result)
	if len(findings) != 1 || findings[0].Code != "TF002" {
		t.Fatalf("Analyze() = %#v, want only TF002", findings)
	}
}
