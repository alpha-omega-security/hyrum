package hyrum

import (
	"context"
	"path/filepath"
	"runtime"

	"github.com/git-pkgs/managers"
)

const pythonVenvDir = ".venv"

// NewPythonVenvManager returns the manager used for isolated PyPI test runs.
// VerifyMatrix only calls Init, Add, Name, and Ecosystem on the returned value.
func NewPythonVenvManager(dir string) managers.Manager {
	return &pythonVenvManager{
		Manager:   nil,
		dir:       dir,
		bootstrap: "python3",
		python:    pythonVenvExecutable(dir),
		runner:    managers.NewExecRunner(),
	}
}

// PythonVenvTestCommand runs pytest with the interpreter created by
// NewPythonVenvManager in scratch.
func PythonVenvTestCommand(scratch string) TestCommand {
	python := pythonVenvExecutable(scratch)
	return func(dir string, _ []string) []string {
		return []string{python, "-m", "pytest", "-q", dir}
	}
}

type pythonVenvManager struct {
	managers.Manager
	dir       string
	bootstrap string
	python    string
	runner    managers.Runner
}

func (m *pythonVenvManager) Name() string { return "pip" }

func (m *pythonVenvManager) Ecosystem() string { return EcoPyPI }

func (m *pythonVenvManager) Init(ctx context.Context) (*managers.Result, error) {
	result, err := m.runner.Run(ctx, m.dir, m.bootstrap, "-m", "venv", pythonVenvDir)
	if err != nil || result == nil || !result.Success() {
		return result, err
	}
	return m.runner.Run(ctx, m.dir,
		m.python, "-m", "pip", "install",
		"--disable-pip-version-check", "--no-input", "--quiet", "--", "pytest",
	)
}

func (m *pythonVenvManager) Add(
	ctx context.Context,
	pkg string,
	opts managers.AddOptions,
) (*managers.Result, error) {
	requirement := pkg
	if opts.Version != "" {
		requirement += "==" + opts.Version
	}
	return m.runner.Run(ctx, m.dir,
		m.python, "-m", "pip", "install",
		"--disable-pip-version-check", "--no-input", "--quiet", "--force-reinstall", "--", requirement,
	)
}

func pythonVenvExecutable(root string) string {
	if absolute, err := filepath.Abs(root); err == nil {
		root = absolute
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(root, pythonVenvDir, "Scripts", "python.exe")
	}
	return filepath.Join(root, pythonVenvDir, "bin", "python")
}
