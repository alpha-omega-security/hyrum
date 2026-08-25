package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/git-pkgs/managers"
)

func TestGoTestRunnerVerbose(t *testing.T) {
	// parseTestOutput counts Go results by matching "^--- PASS:" and
	// "^--- FAIL:" lines, which go test emits only under -v. Without it a
	// passing package prints only "ok <pkg>" and verify reports 0 pass/0 fail.
	argv := testRunners[hyrum.EcoGo](".", nil)
	joined := strings.Join(argv, " ")
	if !slices.Contains(argv, "-v") {
		t.Fatalf("go test runner argv %q must include -v so parseTestOutput can count results", joined)
	}
}

func TestSplitDepSpec(t *testing.T) {
	cases := []struct{ in, name, ver string }{
		{"ws", "ws", ""},
		{"ws@8.17.1", "ws", "8.17.1"},
		{"@scope/pkg", "@scope/pkg", ""},
		{"@scope/pkg@1.2.3", "@scope/pkg", "1.2.3"},
		{"flask@2.3.3", "flask", "2.3.3"},
	}
	for _, c := range cases {
		n, v := splitDepSpec(c.in)
		if n != c.name || v != c.ver {
			t.Errorf("%q: got (%q,%q) want (%q,%q)", c.in, n, v, c.name, c.ver)
		}
	}
}

func TestCheckOneRejectsUnsafeDependencyPath(t *testing.T) {
	if _, err := checkOne(context.Background(), nil, t.TempDir(), "../../escape"); err == nil {
		t.Fatal("checkOne accepted a dependency name that escapes the tests root")
	}
}

func TestCheckOneRejectsMissingSuite(t *testing.T) {
	testsRoot := t.TempDir()
	target := &hyrum.Target{Deps: []hyrum.Dep{{Name: "example", Ecosystem: hyrum.EcoNPM}}}
	_, err := checkOne(t.Context(), target, testsRoot, "example@1.0.0")
	if err == nil {
		t.Fatal("checkOne accepted a missing test suite")
	}
	if !strings.Contains(err.Error(), filepath.Join(testsRoot, "example")) {
		t.Fatalf("checkOne error = %q", err)
	}
}

func TestReadGeneratedFilesRecursesAndExcludesMetadata(t *testing.T) {
	testDir := filepath.Join(t.TempDir(), "tests", "hyrum", "example")
	nested := filepath.Join(testDir, "from_repo", "websocket")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(nested, "message.test.js")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "meta.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := readGeneratedFiles(testDir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("from_repo", "websocket", "message.test.js")
	if len(files) != 1 || files[0].Path != want || files[0].Content != "test content" {
		t.Fatalf("readGeneratedFiles = %#v, want path %q with test content", files, want)
	}
}

func TestReadGeneratedFilesRejectsEmptySuite(t *testing.T) {
	testDir := filepath.Join(t.TempDir(), "tests", "hyrum", "example")
	if err := os.MkdirAll(testDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "meta.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := readGeneratedFiles(testDir)
	if err == nil || !strings.Contains(err.Error(), "no runnable test files") {
		t.Fatalf("readGeneratedFiles error = %v", err)
	}
}

func TestCmdCheckDoesNotRequireTargetPackageManager(t *testing.T) {
	target := t.TempDir()
	pyproject := `[project]
name = "consumer"
version = "0.1.0"
dependencies = ["idna==3.7"]

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"
`
	if err := os.WriteFile(filepath.Join(target, "pyproject.toml"), []byte(pyproject), 0o644); err != nil {
		t.Fatal(err)
	}

	err := cmdCheck(t.Context(), []string{
		"--dep", "idna@3.7",
		"--tests", filepath.Join(target, "missing-tests"),
		target,
	})
	if err == nil || !strings.Contains(err.Error(), "no tests at") {
		t.Fatalf("cmdCheck error = %v, want missing-suite error after target analysis", err)
	}
}

func TestConstraintVersion(t *testing.T) {
	cases := []struct{ in, ecosystem, want string }{
		{"^1.2.3", hyrum.EcoNPM, "1.2.3"},
		{"~> 4.0", hyrum.EcoGem, "4.0"},
		{"~=2.3", hyrum.EcoPyPI, "2.3"},
		{">=2.3,!=2.3", hyrum.EcoPyPI, ""},
		{"*", hyrum.EcoNPM, ""},
	}
	for _, test := range cases {
		if got := constraintVersion(test.in, test.ecosystem); got != test.want {
			t.Errorf("constraintVersion(%q, %q) = %q, want %q", test.in, test.ecosystem, got, test.want)
		}
	}
}

func TestDetectManagerForGoUsesGomod(t *testing.T) {
	mgr, err := detectManagerFor(t.TempDir(), hyrum.EcoGo)
	if err != nil {
		t.Fatal(err)
	}
	if mgr.Name() != "gomod" {
		t.Fatalf("detected manager = %q, want gomod", mgr.Name())
	}
}

func TestVerificationRuntimePyPIUsesScratchVirtualenv(t *testing.T) {
	scratch := t.TempDir()
	mgr, testCommand, err := verificationRuntime(scratch, hyrum.EcoPyPI)
	if err != nil {
		t.Fatal(err)
	}
	if mgr.Name() != "pip" || mgr.Ecosystem() != hyrum.EcoPyPI {
		t.Fatalf("manager = %s/%s, want pip/%s", mgr.Name(), mgr.Ecosystem(), hyrum.EcoPyPI)
	}
	argv := testCommand(".", nil)
	if len(argv) == 0 || !strings.HasPrefix(argv[0], filepath.Join(scratch, ".venv")) {
		t.Fatalf("test command = %q, want interpreter under scratch virtualenv", argv)
	}
}

type addErrorManager struct {
	managers.Manager
	onAdd func(string, managers.AddOptions)
}

func (addErrorManager) Name() string      { return "test" }
func (addErrorManager) Ecosystem() string { return hyrum.EcoNPM }
func (addErrorManager) Init(context.Context) (*managers.Result, error) {
	return &managers.Result{}, nil
}
func (m addErrorManager) Add(_ context.Context, name string, opts managers.AddOptions) (*managers.Result, error) {
	if m.onAdd != nil {
		m.onAdd(name, opts)
	}
	return nil, errors.New("invalid package")
}

func TestCheckOneUsesAndRemovesScratchDirectory(t *testing.T) {
	testsRoot := t.TempDir()
	testDir := filepath.Join(testsRoot, "example")
	if err := os.Mkdir(testDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "example.test.js"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := &hyrum.Target{
		Path: t.TempDir(),
		Deps: []hyrum.Dep{{Name: "example", Version: "1.0.0", Ecosystem: hyrum.EcoNPM}},
	}
	var scratch string
	factory := func(dir, ecosystem string) (managers.Manager, hyrum.TestCommand, error) {
		scratch = dir
		if ecosystem != hyrum.EcoNPM {
			t.Fatalf("ecosystem = %q, want %q", ecosystem, hyrum.EcoNPM)
		}
		mgr := addErrorManager{onAdd: func(name string, opts managers.AddOptions) {
			if name != "example" || opts.Version != "1.0.0" || !opts.Exact {
				t.Errorf("add = %s %+v, want example at exact version 1.0.0", name, opts)
			}
		}}
		return mgr, func(string, []string) []string { return nil }, nil
	}

	ok, err := checkOneWithRuntime(t.Context(), target, testsRoot, "example", factory)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("checkOne reported a failed install as successful")
	}
	if scratch == "" || scratch == target.Path {
		t.Fatalf("scratch = %q, target = %q", scratch, target.Path)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch directory still exists: %v", err)
	}
}
