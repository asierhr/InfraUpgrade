package scanner

import (
	"os"
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

func TestScanInfersProviderWithoutDeclaration(t *testing.T) {
	root := t.TempDir()
	terraform := `
provider "aws" {
  region = "eu-west-3"
}
resource "aws_instance" "web" {}
data "aws_caller_identity" "current" {}
`
	lock := `
provider "registry.terraform.io/hashicorp/aws" {
  version = "6.40.0"
}
`

	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(terraform), 0o600); err != nil {
		t.Fatalf("write main.tf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".terraform.lock.hcl"), []byte(lock), 0o600); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	result, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Providers) != 1 {
		t.Fatalf("len(Providers) = %d, want 1", len(result.Providers))
	}

	provider := result.Providers[0]
	if provider.Name != "aws" || provider.Source != "hashicorp/aws" ||
		provider.Declared || !provider.Configured || !provider.Inferred ||
		provider.ResourceCount != 2 || provider.LockedVersion != "6.40.0" {
		t.Fatalf("provider = %#v", provider)
	}
}

func TestScanRejectsInvalidTerraform(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "broken.tf"),
		[]byte(`resource "aws_instance" {`),
		0o600,
	); err != nil {
		t.Fatalf("write broken.tf: %v", err)
	}

	if _, err := Scan(root); err == nil {
		t.Fatal("Scan() error = nil, want parse error")
	}
}

func TestReadLockedVersion(t *testing.T) {
	root := t.TempDir()
	lock := `
provider "registry.terraform.io/hashicorp/aws" {
  version = "6.40.0"
}

provider "registry.terraform.io/hashicorp/random" {
  version = "3.7.2"
}
`
	if err := os.WriteFile(filepath.Join(root, ".terraform.lock.hcl"), []byte(lock), 0o600); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	versions, err := ReadLockedVersion(root)
	if err != nil {
		t.Fatalf("ReadLockedVersion() error = %v", err)
	}
	if versions["hashicorp/aws"] != "6.40.0" || versions["hashicorp/random"] != "3.7.2" {
		t.Fatalf("versions = %#v", versions)
	}
}

func TestReadLockedVersionWithoutLockfile(t *testing.T) {
	versions, err := ReadLockedVersion(t.TempDir())
	if err != nil {
		t.Fatalf("ReadLockedVersion() error = %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("versions = %#v, want empty", versions)
	}
}

func TestReadLockedVersionRejectsInvalidLockfile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".terraform.lock.hcl"), []byte("invalid lock"), 0o600); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	if _, err := ReadLockedVersion(root); err == nil {
		t.Fatal("ReadLockedVersion() error = nil, want parse error")
	}
}
