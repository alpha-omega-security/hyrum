package hyrum

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/git-pkgs/managers"
)

func TestPythonVenvManagerInitializesAndAddsExactVersion(t *testing.T) {
	runner := managers.NewMockRunner()
	dir := t.TempDir()
	python := pythonVenvExecutable(dir)
	mgr := &pythonVenvManager{
		dir:       dir,
		bootstrap: "python3",
		python:    python,
		runner:    runner,
	}

	if _, err := mgr.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Add(t.Context(), "example", managers.AddOptions{Version: "1.2.3", Exact: true}); err != nil {
		t.Fatal(err)
	}

	want := [][]string{
		{"python3", "-m", "venv", pythonVenvDir},
		{python, "-m", "pip", "install", "--disable-pip-version-check", "--no-input", "--quiet", "--", "pytest"},
		{python, "-m", "pip", "install", "--disable-pip-version-check", "--no-input", "--quiet", "--force-reinstall", "--", "example==1.2.3"},
	}
	if !reflect.DeepEqual(runner.Captured, want) {
		t.Fatalf("commands = %#v, want %#v", runner.Captured, want)
	}
}

func TestPythonVenvTestCommandUsesScratchInterpreter(t *testing.T) {
	scratch := t.TempDir()
	got := PythonVenvTestCommand(scratch)("tests", nil)
	want := []string{pythonVenvExecutable(scratch), "-m", "pytest", "-q", "tests"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got[0]) {
		t.Fatalf("interpreter path = %q, want absolute", got[0])
	}
}
