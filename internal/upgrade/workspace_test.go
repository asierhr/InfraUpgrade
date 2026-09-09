package upgrade

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyProjectCopiesFilesAndSkipsGeneratedContent(t *testing.T) {
	source := t.TempDir()
	mustWriteFile(t, filepath.Join(source, "main.tf"), "resource {}")
	mustWriteFile(t, filepath.Join(source, "modules", "local", "main.tf"), "module content")
	mustWriteFile(t, filepath.Join(source, ".terraform", "provider.bin"), "ignored")
	mustWriteFile(t, filepath.Join(source, ".git", "config"), "ignored")
	mustWriteFile(t, filepath.Join(source, "infraupgrade.tfplan"), "ignored")
	mustWriteFile(t, filepath.Join(source, ".terraform.tfstate.lock.info"), "ignored")

	workspace, cleanup, err := copyProject(source)
	if err != nil {
		t.Fatalf("copyProject() error = %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	assertFileContent(t, filepath.Join(workspace, "main.tf"), "resource {}")
	assertFileContent(t, filepath.Join(workspace, "modules", "local", "main.tf"), "module content")
	assertPathMissing(t, filepath.Join(workspace, ".terraform"))
	assertPathMissing(t, filepath.Join(workspace, ".git"))
	assertPathMissing(t, filepath.Join(workspace, "infraupgrade.tfplan"))
	assertPathMissing(t, filepath.Join(workspace, ".terraform.tfstate.lock.info"))

	temporaryRoot := filepath.Dir(workspace)
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	assertPathMissing(t, temporaryRoot)
}

func TestCopyProjectRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.tf")
	mustWriteFile(t, path, "resource {}")

	if _, _, err := copyProject(path); err == nil {
		t.Fatal("copyProject() error = nil")
	}
}

func TestReadOptionalFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	content, err := readOptionalFile(missing)
	if err != nil || content != nil {
		t.Fatalf("readOptionalFile(missing) = %q, %v", content, err)
	}

	path := filepath.Join(t.TempDir(), "file")
	mustWriteFile(t, path, "content")
	content, err = readOptionalFile(path)
	if err != nil || string(content) != "content" {
		t.Fatalf("readOptionalFile(file) = %q, %v", content, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if string(content) != want {
		t.Fatalf("content of %s = %q, want %q", path, content, want)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("path %s exists or returned unexpected error: %v", path, err)
	}
}
