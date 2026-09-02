package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/git-pkgs/managers"
	"github.com/git-pkgs/managers/definitions"
	"github.com/git-pkgs/vers"
)

// cmdCheck installs one or more dependencies at given versions in isolated
// scratch environments and runs the Hyrum's tests under tests/hyrum/<dep>/
// against each. Exit is non-zero when any suite fails.
func cmdCheck(ctx context.Context, args []string) error {
	fs := newFlags("check")
	var deps stringList
	fs.Var(&deps, "dep", "dependency as name or name@version (repeatable)")
	root := fs.String("tests", "tests/hyrum", "root of generated tests, relative to the target or absolute")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := "."
	if fs.NArg() > 0 {
		path = fs.Arg(0)
	}
	if len(deps) == 0 {
		return fmt.Errorf("at least one --dep is required")
	}

	t, err := hyrum.Analyze(path)
	if err != nil {
		return err
	}
	testsRoot := outRoot(t.Path, *root)
	failed := false
	for _, spec := range deps {
		ok, err := checkOne(ctx, t, testsRoot, spec)
		if err != nil {
			return err
		}
		failed = failed || !ok
	}
	if failed {
		return fmt.Errorf("one or more checks failed")
	}
	return nil
}

type verificationRuntimeFactory func(string, string) (managers.Manager, hyrum.TestCommand, error)

// checkOne installs one dep spec in a fresh scratch directory and runs the
// tests generated for it. The analyzed checkout is only read.
func checkOne(ctx context.Context, t *hyrum.Target, testsRoot, spec string) (bool, error) {
	return checkOneWithRuntime(ctx, t, testsRoot, spec, verificationRuntime)
}

func checkOneWithRuntime(
	ctx context.Context,
	t *hyrum.Target,
	testsRoot, spec string,
	runtimeFactory verificationRuntimeFactory,
) (bool, error) {
	name, version := splitDepSpec(spec)
	if err := validateRelativePath("dependency name", name); err != nil {
		return false, err
	}
	d, ok := findDep(t, name)
	if !ok || d.Ecosystem == "" {
		return false, fmt.Errorf("%s: cannot determine dependency ecosystem", name)
	}
	if version == "" {
		version = constraintVersion(d.Version, d.Ecosystem)
		if version == "" {
			return false, fmt.Errorf("%s: no installable version found; pass --dep %s@<version>", name, name)
		}
	}

	testDir := filepath.Join(testsRoot, name)
	files, err := readGeneratedFiles(testDir)
	if err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}

	scratch, err := os.MkdirTemp("", "hyrum-check-")
	if err != nil {
		return false, fmt.Errorf("%s: create scratch directory: %w", name, err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	mgr, testCommand, err := runtimeFactory(scratch, d.Ecosystem)
	if err != nil {
		return false, fmt.Errorf("%s: verification runtime: %w", name, err)
	}

	fmt.Fprintf(os.Stderr, "→ %s add %s@%s in scratch\n", safeLine(mgr.Name()), safeLine(name), safeLine(version))
	results := hyrum.VerifyMatrix(ctx, mgr, testCommand, scratch, name, files, []string{version})
	if len(results) != 1 {
		return false, fmt.Errorf("%s@%s: verification returned %d results", name, version, len(results))
	}
	result := results[0]
	passed := result.Error == "" && result.Fail == 0 && result.Pass > 0
	status := "PASS"
	if !passed {
		status = "FAIL"
	}
	fmt.Printf("%s@%s: %s\n", safeLine(name), safeLine(version), status)
	printVerificationDetails(result)
	return passed, nil
}

func printVerificationDetails(result hyrum.VerifyResult) {
	if result.Output != "" {
		if result.Error != "" {
			fmt.Println(indent(safeLine(result.Error)))
		}
		fmt.Println(indent(safeText(result.Output)))
		return
	}
	if result.Error != "" {
		fmt.Println(indent(safeText(result.Error)))
	}
}

func readGeneratedFiles(testDir string) ([]hyrum.GeneratedFile, error) {
	info, err := os.Stat(testDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no tests at %s", testDir)
		}
		return nil, fmt.Errorf("inspect tests at %s: %w", testDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("test suite path is not a directory: %s", testDir)
	}

	var files []hyrum.GeneratedFile
	err = filepath.WalkDir(testDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() || filepath.Ext(path) == ".json" {
			return nil
		}
		rel, err := filepath.Rel(testDir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, hyrum.GeneratedFile{Path: rel, Content: string(content)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read tests at %s: %w", testDir, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no runnable test files under %s", testDir)
	}
	return files, nil
}

// testRunners maps a purl ecosystem type to the command that runs tests in a
// scratch directory. files contains relative paths for runners that require
// explicit test files.
var testRunners = map[string]func(dir string, files []string) []string{
	hyrum.EcoNPM: func(_ string, files []string) []string { return append([]string{"node", "--test"}, files...) },
	hyrum.EcoGo:  func(dir string, _ []string) []string { return []string{"go", "test", "-v", "./" + dir + "/..."} },
}

func verificationRuntime(scratch, ecosystem string) (managers.Manager, hyrum.TestCommand, error) {
	if ecosystem == hyrum.EcoPyPI {
		return hyrum.NewPythonVenvManager(scratch), hyrum.PythonVenvTestCommand(scratch), nil
	}
	testCommand, ok := testRunners[ecosystem]
	if !ok {
		return nil, nil, fmt.Errorf("no test runner for ecosystem %q", ecosystem)
	}
	mgr, err := detectManagerFor(scratch, ecosystem)
	if err != nil {
		return nil, nil, err
	}
	return mgr, hyrum.TestCommand(testCommand), nil
}

func detectManager(dir, hint string) (managers.Manager, error) {
	defs, err := definitions.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	det := managers.NewDetector(managers.NewTranslator(), managers.NewExecRunner())
	for _, d := range defs {
		det.Register(d)
	}
	return det.Detect(dir, managers.DetectOptions{Manager: hint})
}

// ecosystemManager maps a purl ecosystem type to the managers definition name
// used for a fresh scratch directory. Where several managers serve one
// ecosystem the entry is the one whose Init produces a manifest that Add can
// then target without further setup.
var ecosystemManager = map[string]string{
	hyrum.EcoNPM:      "npm",
	hyrum.EcoGo:       "gomod",
	hyrum.EcoGem:      "bundler",
	hyrum.EcoCargo:    "cargo",
	hyrum.EcoComposer: "composer",
	hyrum.EcoHex:      "mix",
}

// constraintVersion returns the inclusive lower bound of a native version
// constraint. Scratch verification needs a concrete version to install.
func constraintVersion(v, ecosystem string) string {
	if v == "" {
		return ""
	}
	rangeValue, err := vers.ParseNative(v, ecosystem)
	if err != nil {
		return ""
	}
	minimum, ok := rangeValue.MinimumVersion()
	if !ok {
		return ""
	}
	return minimum
}

// detectManagerFor constructs a manager for ecosystem in dir. dir may be
// empty; the caller is expected to call mgr.Init before Add.
func detectManagerFor(dir, ecosystem string) (managers.Manager, error) {
	name, ok := ecosystemManager[ecosystem]
	if !ok {
		return nil, fmt.Errorf("no default package manager mapped for ecosystem %q", ecosystem)
	}
	return detectManager(dir, name)
}

func splitDepSpec(s string) (name, version string) {
	// Handle scoped npm packages: @scope/name@version has two @s.
	at := strings.LastIndex(s, "@")
	if at <= 0 {
		return s, ""
	}
	return s[:at], s[at+1:]
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}
