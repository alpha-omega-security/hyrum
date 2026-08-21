package hyrum

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/git-pkgs/brief"
)

func TestDetectTargetNameUsesRootManifestPackageName(t *testing.T) {
	targetPath := t.TempDir()
	writeTargetFile(t, filepath.Join(targetPath, "pyproject.toml"), "[project]\nname = \"apache-airflow-core\"\n")
	report := &brief.Report{Git: &brief.GitInfo{Remotes: map[string]string{
		"origin": "https://github.com/apache/airflow.git",
	}}}

	if got := detectTargetName(targetPath, report); got != "apache-airflow-core" {
		t.Fatalf("target name = %q, want apache-airflow-core", got)
	}
}

func TestDetectTargetNameFallsBackForAmbiguousRootPackages(t *testing.T) {
	targetPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(targetPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTargetFile(t, filepath.Join(targetPath, "package.json"), `{"name":"web-client"}`)
	writeTargetFile(t, filepath.Join(targetPath, "go.mod"), "module example.com/service\n")
	report := &brief.Report{Git: &brief.GitInfo{Remotes: map[string]string{
		"origin": "https://github.com/example/project.git",
	}}}

	if got := detectTargetName(targetPath, report); got != "project" {
		t.Fatalf("target name = %q, want project", got)
	}
}

func TestDetectTargetNameDistinguishesNestedFallbackPaths(t *testing.T) {
	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(repository, "packages", "one", "shared")
	second := filepath.Join(repository, "packages", "two", "shared")
	for _, targetPath := range []string{first, second} {
		if err := os.MkdirAll(targetPath, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	report := &brief.Report{Git: &brief.GitInfo{Remotes: map[string]string{
		"origin": "https://github.com/example/project.git",
	}}}

	firstName := detectTargetName(first, report)
	secondName := detectTargetName(second, report)
	if firstName == secondName {
		t.Fatalf("nested target names collide: %q", firstName)
	}
	for _, name := range []string{firstName, secondName} {
		if strings.ContainsAny(name, `/\\`) {
			t.Fatalf("target name contains a path separator: %q", name)
		}
	}
}

func TestNormalizeTargetNamePreservesChangedIdentity(t *testing.T) {
	scoped := normalizeTargetName("@scope/package")
	unscoped := normalizeTargetName("scope-package")
	if scoped == unscoped {
		t.Fatalf("normalized target names collide: %q", scoped)
	}
	if strings.ContainsAny(scoped, `/\\@`) {
		t.Fatalf("normalized target name is not one component: %q", scoped)
	}
	if got := normalizeTargetName("apache-airflow-task-sdk"); got != "apache-airflow-task-sdk" {
		t.Fatalf("safe target name changed to %q", got)
	}
}

func writeTargetFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
