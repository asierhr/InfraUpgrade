package schemadiff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseSnapshot(t *testing.T) {
	snapshot, err := ParseSnapshot([]byte(`{
  "format_version": "1.0",
  "provider_schemas": {
    "registry.terraform.io/hashicorp/aws": {
      "resource_schemas": {
        "aws_instance": {
          "block": {
            "attributes": {
              "ami": {"type": "string", "required": true}
            }
          }
        }
      }
    }
  }
}`))
	if err != nil {
		t.Fatalf("ParseSnapshot() error = %v", err)
	}
	resource := snapshot.Resources["aws_instance"]
	if resource.Provider != "registry.terraform.io/hashicorp/aws" || !resource.Attributes["ami"].Required {
		t.Fatalf("ParseSnapshot() = %#v", snapshot)
	}
}

func TestParseSnapshotRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "invalid JSON", content: `{`, want: "decode provider schema JSON"},
		{name: "missing format", content: `{}`, want: "has no format version"},
		{name: "unsupported format", content: `{"format_version":"2.0"}`, want: "unsupported provider schema format"},
		{
			name: "duplicate resource",
			content: `{
  "format_version": "1.0",
  "provider_schemas": {
    "provider-one": {"resource_schemas":{"shared":{"block":{}}}},
    "provider-two": {"resource_schemas":{"shared":{"block":{}}}}
  }
}`,
			want: "resource shared is exposed by providers",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseSnapshot([]byte(test.content))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseSnapshot() error = %v; want containing %q", err, test.want)
			}
		})
	}
}

func TestScanAssigmentsFindsResourceAttributes(t *testing.T) {
	root := t.TempDir()
	writeSchemaDiffFixture(t, filepath.Join(root, "z.tf"), `
data "aws_ami" "ignored" {
  owners = ["self"]
}

resource "aws_instance" "web" {
  instance_type = var.instance_type
  ami           = "ami-test"

  root_block_device {
    volume_size = 20
  }
}
`)
	writeSchemaDiffFixture(t, filepath.Join(root, "a.tf"), `
resource "aws_s3_bucket" "assets" {
  bucket = local.bucket_name
}
`)

	assignments, err := ScanAssigments(root)
	if err != nil {
		t.Fatalf("ScanAssigments() error = %v", err)
	}
	if len(assignments) != 4 {
		t.Fatalf("ScanAssigments() = %#v", assignments)
	}
	want := []Assignment{
		{File: "a.tf", ResourceType: "aws_s3_bucket", ResourceName: "assets", Attribute: "bucket", Expression: "local.bucket_name"},
		{File: "z.tf", ResourceType: "aws_instance", ResourceName: "web", Attribute: "ami", Expression: `"ami-test"`},
		{File: "z.tf", ResourceType: "aws_instance", ResourceName: "web", Attribute: "instance_type", Expression: "var.instance_type"},
		{File: "z.tf", ResourceType: "aws_instance", ResourceName: "web", BlockPath: []string{"root_block_device"}, Attribute: "volume_size", Expression: "20"},
	}
	if !reflect.DeepEqual(assignments, want) {
		t.Fatalf("ScanAssigments() = %#v; want %#v", assignments, want)
	}
}

func TestAssignmentFullPath(t *testing.T) {
	tests := []struct {
		name       string
		assignment Assignment
		want       string
	}{
		{name: "top level", assignment: Assignment{Attribute: "ami"}, want: "ami"},
		{name: "nested", assignment: Assignment{BlockPath: []string{"network_configuration", "vpc_config"}, Attribute: "subnet_ids"}, want: "network_configuration.vpc_config.subnet_ids"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.assignment.FullPath(); got != test.want {
				t.Fatalf("FullPath() = %q; want %q", got, test.want)
			}
		})
	}
}

func TestScanAssigmentsFindsDynamicBlockContent(t *testing.T) {
	root := t.TempDir()
	writeSchemaDiffFixture(t, filepath.Join(root, "main.tf"), `
resource "aws_instance" "web" {
  dynamic "root_block_device" {
    for_each = var.disks

    content {
      volume_size = root_block_device.value.size
    }
  }
}
`)

	assignments, err := ScanAssigments(root)
	if err != nil {
		t.Fatalf("ScanAssigments() error = %v", err)
	}
	want := []Assignment{{
		File: "main.tf", ResourceType: "aws_instance", ResourceName: "web",
		BlockPath: []string{"root_block_device"}, Attribute: "volume_size",
		Expression: "root_block_device.value.size",
	}}
	if !reflect.DeepEqual(assignments, want) {
		t.Fatalf("ScanAssigments() = %#v; want %#v", assignments, want)
	}
}

func TestScanAssigmentsRejectsInvalidHCL(t *testing.T) {
	root := t.TempDir()
	writeSchemaDiffFixture(t, filepath.Join(root, "main.tf"), `resource "aws_instance" "web" {`)
	_, err := ScanAssigments(root)
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("ScanAssigments() error = %v", err)
	}
}

func TestCompareAssignmentsDetectsRelevantChanges(t *testing.T) {
	assignments := []Assignment{
		{File: "a.tf", ResourceType: "removed_resource", ResourceName: "one", Attribute: "name"},
		{File: "b.tf", ResourceType: "example", ResourceName: "two", Attribute: "removed"},
		{File: "b.tf", ResourceType: "example", ResourceName: "two", Attribute: "typed"},
		{File: "b.tf", ResourceType: "example", ResourceName: "two", Attribute: "computed"},
		{File: "b.tf", ResourceType: "example", ResourceName: "two", Attribute: "unknown_to_baseline"},
	}
	baseline := Snapshot{Resources: map[string]ResourceSchema{
		"removed_resource": {Attributes: map[string]AttributeSchema{"name": {Type: json.RawMessage(`"string"`), Optional: true}}},
		"example": {Attributes: map[string]AttributeSchema{
			"removed":  {Type: json.RawMessage(`"string"`), Optional: true},
			"typed":    {Type: json.RawMessage(`"string"`), Optional: true},
			"computed": {Type: json.RawMessage(`"bool"`), Optional: true},
		}},
	}}
	upgraded := Snapshot{Resources: map[string]ResourceSchema{
		"example": {Attributes: map[string]AttributeSchema{
			"typed":    {Type: json.RawMessage(`["list", "string"]`), Required: true},
			"computed": {Type: json.RawMessage(`"bool"`), Computed: true},
		}},
	}}

	changes, err := CompareAssignments(assignments, baseline, upgraded)
	if err != nil {
		t.Fatalf("CompareAssignments() error = %v", err)
	}
	counts := make(map[ChangeKind]int)
	for _, change := range changes {
		counts[change.Kind]++
	}
	wantCounts := map[ChangeKind]int{
		ResourceRemoved: 1, AttributeRemoved: 1, AttributeTypeChanged: 1,
		ConfigurabilityLost: 1, RequirementChanged: 2,
	}
	if !reflect.DeepEqual(counts, wantCounts) {
		t.Fatalf("CompareAssignments() kinds = %#v; want %#v", counts, wantCounts)
	}
}

func TestCompareAssignmentsDetectsNestedChanges(t *testing.T) {
	assignment := Assignment{
		File: "main.tf", ResourceType: "aws_instance", ResourceName: "web",
		BlockPath: []string{"root_block_device"}, Attribute: "volume_size",
	}
	attribute := AttributeSchema{Type: json.RawMessage(`"number"`), Optional: true}
	baseline := Snapshot{Resources: map[string]ResourceSchema{
		"aws_instance": {
			BlockTypes: map[string]NestedBlockSchema{
				"root_block_device": {
					NestingMode: "list",
					Block:       BlockSchema{Attributes: map[string]AttributeSchema{"volume_size": attribute}},
				},
			},
		},
	}}

	t.Run("nesting mode changed", func(t *testing.T) {
		upgraded := Snapshot{Resources: map[string]ResourceSchema{
			"aws_instance": {
				BlockTypes: map[string]NestedBlockSchema{
					"root_block_device": {
						NestingMode: "set",
						Block:       BlockSchema{Attributes: map[string]AttributeSchema{"volume_size": attribute}},
					},
				},
			},
		}}
		changes, err := CompareAssignments([]Assignment{assignment}, baseline, upgraded)
		if err != nil {
			t.Fatalf("CompareAssignments() error = %v", err)
		}
		if len(changes) != 1 || changes[0].Kind != NestingModeChanged || changes[0].BeforeNestingMode != "list" || changes[0].AfterNestingMode != "set" {
			t.Fatalf("CompareAssignments() = %#v", changes)
		}
	})

	t.Run("nested block removed", func(t *testing.T) {
		upgraded := Snapshot{Resources: map[string]ResourceSchema{
			"aws_instance": {},
		}}
		changes, err := CompareAssignments([]Assignment{assignment}, baseline, upgraded)
		if err != nil {
			t.Fatalf("CompareAssignments() error = %v", err)
		}
		if len(changes) != 1 || changes[0].Kind != NestedBlockRemoved || changes[0].Assignment.FullPath() != "root_block_device.volume_size" {
			t.Fatalf("CompareAssignments() = %#v", changes)
		}
	})
}

func TestDetectFindsNestedAttributeTypeChange(t *testing.T) {
	root := t.TempDir()
	writeSchemaDiffFixture(t, filepath.Join(root, "main.tf"), `
resource "aws_instance" "web" {
  root_block_device {
    volume_size = 20
  }
}
`)
	baseline := nestedSchemaDocumentForTest("list", `"number"`)
	upgraded := nestedSchemaDocumentForTest("list", `"string"`)

	report, err := Detect(root, baseline, upgraded)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(report.Changes) != 1 || report.Changes[0].Kind != AttributeTypeChanged {
		t.Fatalf("Detect() changes = %#v", report.Changes)
	}
	change := report.Changes[0]
	if change.Assignment.FullPath() != "root_block_device.volume_size" || change.BeforeType != `"number"` || change.AfterType != `"string"` {
		t.Fatalf("nested assignment change = %#v", change)
	}
}

func TestDetectCombinesSchemaAndProjectScanning(t *testing.T) {
	root := t.TempDir()
	writeSchemaDiffFixture(t, filepath.Join(root, "main.tf"), `
resource "aws_instance" "web" {
  ami = var.ami
}
`)
	baseline := schemaDocumentForTest(`"string"`)
	upgraded := schemaDocumentForTest(`["list","string"]`)

	report, err := Detect(root, baseline, upgraded)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(report.Assigments) != 1 || len(report.Changes) != 1 {
		t.Fatalf("Detect() = %#v", report)
	}
	if report.Changes[0].Kind != AttributeTypeChanged || report.Changes[0].Assignment.Expression != "var.ami" {
		t.Fatalf("Detect() change = %#v", report.Changes[0])
	}
}

func TestTypeSignatureNormalizesEquivalentJSON(t *testing.T) {
	left := typeSignature(json.RawMessage(`["list", "string"]`))
	right := typeSignature(json.RawMessage(`[ "list","string" ]`))
	if left != right || left != `["list","string"]` {
		t.Fatalf("type signatures = %q and %q", left, right)
	}
}

func schemaDocumentForTest(attributeType string) []byte {
	return []byte(`{
  "format_version":"1.0",
  "provider_schemas":{
    "registry.terraform.io/hashicorp/aws":{
      "resource_schemas":{
        "aws_instance":{
          "block":{
            "attributes":{
              "ami":{"type":` + attributeType + `,"optional":true}
            }
          }
        }
      }
    }
  }
}`)
}

func nestedSchemaDocumentForTest(nestingMode string, attributeType string) []byte {
	return []byte(`{
  "format_version":"1.0",
  "provider_schemas":{
    "registry.terraform.io/hashicorp/aws":{
      "resource_schemas":{
        "aws_instance":{
          "block":{
            "block_types":{
              "root_block_device":{
                "nesting_mode":"` + nestingMode + `",
                "block":{
                  "attributes":{
                    "volume_size":{"type":` + attributeType + `,"optional":true}
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}`)
}

func writeSchemaDiffFixture(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
