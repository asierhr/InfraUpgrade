package scanner

import (
	"path/filepath"
	"testing"
)

func TestScan(t *testing.T) {
	result, err := Scan(filepath.Join("testdata", "basic"))
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if result.RequiredVersion != ">= 1.6.0" {
		t.Fatalf("RequiredVersion = %q, want %q", result.RequiredVersion, ">= 1.6.0")
	}
	if len(result.TerraformFiles) != 1 || result.TerraformFiles[0] != "versions.tf" {
		t.Fatalf("TerraformFiles = %#v, want [versions.tf]", result.TerraformFiles)
	}
	if len(result.Providers) != 2 {
		t.Fatalf("len(Providers) = %d, want 2", len(result.Providers))
	}

	aws := result.Providers[0]
	if aws.Name != "aws" || aws.Source != "hashicorp/aws" || aws.Constraint != "~> 5.0" || aws.LockedVersion != "5.82.0" {
		t.Fatalf("AWS provider = %#v", aws)
	}
	random := result.Providers[1]
	if random.Name != "random" || random.Source != "hashicorp/random" || random.Constraint != ">= 3.6.0" || random.LockedVersion != "3.6.3" {
		t.Fatalf("Random provider = %#v", random)
	}
}

func TestScanRejectsFile(t *testing.T) {
	_, err := Scan(filepath.Join("testdata", "basic", "versions.tf"))
	if err == nil {
		t.Fatal("Scan() error = nil, want an error")
	}
}
