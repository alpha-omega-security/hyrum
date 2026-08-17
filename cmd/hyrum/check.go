package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/git-pkgs/managers"
	"github.com/git-pkgs/managers/definitions"
)

// cmdCheck installs one or more dependencies at given versions and runs the
// Hyrum's tests under tests/hyrum/<dep>/ against each. Exit is non-zero when
// any version fails tests that another version passed.
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
	mgr, err := detectManager(t.Path, managerHint(t))
	if err != nil {
		return fmt.Errorf("detect package manager: %w", err)
	}

	testsRoot := outRoot(t.Path, *root)
	failed := false
	for _, spec := range deps {
		ok, err := checkOne(ctx, t, mgr, testsRoot, spec)
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

// managerHint returns the package-manager name to hand to managers.Detect so
// its ranking (which prefers bun over npm for a bare package.json) does not
// override what a lockfile or config already indicates. brief titles some
// names ("Bun"); managers keys are lowercase and occasionally use a
// different identifier.
func managerHint(t *hyrum.Target) string {
	for _, pm := range t.Report.PackageManagers {
		if pm.Lockfile == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(t.Path, pm.Lockfile)); err == nil {
			return managerName(pm.Name)
		}
	}
	if len(t.Report.PackageManagers) > 0 {
		return managerName(t.Report.PackageManagers[0].Name)
	}
	return ""
}

func managerName(displayName string) string {
	name := strings.ToLower(displayName)
	if name == "go modules" {
		return "gomod"
	}
	return name
}

// checkOne installs one dep spec and runs the tests generated for it. It
// returns whether the tests passed; a returned error is a hard failure such
// as an unknown ecosystem, distinct from a test failure.
func checkOne(ctx context.Context, t *hyrum.Target, mgr managers.Manager, testsRoot, spec string) (bool, error) {
	name, version := splitDepSpec(spec)
	if err := validateRelativePath("dependency name", name); err != nil {
		return false, err
	}
	d, _ := findDep(t, name)
	if d.Ecosystem == "" {
		d.Ecosystem = mgr.Ecosystem()
	}

	testDir := filepath.Join(testsRoot, name)
	info, err := os.Stat(testDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Errorf("%s: no tests at %s", name, testDir)
		}
		return false, fmt.Errorf("%s: inspect tests at %s: %w", name, testDir, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s: test suite path is not a directory: %s", name, testDir)
	}
	if version != "" {
		fmt.Fprintf(os.Stderr, "→ %s add %s@%s\n", mgr.Name(), name, version)
		if r, err := mgr.Add(ctx, name, managers.AddOptions{Version: version}); err != nil || r == nil || !r.Success() {
			stderr := ""
			if r != nil {
				stderr = r.Stderr
			}
			fmt.Fprintf(os.Stderr, "  install failed: %v %s\n", err, stderr)
			return false, nil
		}
	}

	ok, out, err := runTests(ctx, t.Path, testDir, d.Ecosystem)
	if err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	status := "PASS"
	if !ok {
		status = "FAIL"
	}
	fmt.Printf("%s@%s: %s\n", name, versionOr(version, d.Version), status)
	if !ok {
		fmt.Println(indent(out, "  "))
	}
	return ok, nil
}

// testRunners maps a purl ecosystem type to the command that runs tests under
// a given directory. The command runs with cwd set to the target root so
// relative imports and node_modules resolution behave as they would in CI.
// The dir argument is a relative path; files is the pre-globbed list of test
// files under it for runners that need explicit paths.
var testRunners = map[string]func(dir string, files []string) []string{
	hyrum.EcoNPM:  func(_ string, files []string) []string { return append([]string{"node", "--test"}, files...) },
	hyrum.EcoPyPI: func(dir string, _ []string) []string { return []string{"python3", "-m", "pytest", "-q", dir} },
	hyrum.EcoGo:   func(dir string, _ []string) []string { return []string{"go", "test", "-v", "./" + dir + "/..."} },
}

func runTests(ctx context.Context, targetRoot, testDir, ecosystem string) (ok bool, output string, err error) {
	rel, _ := filepath.Rel(targetRoot, testDir)
	build, known := testRunners[ecosystem]
	if !known {
		return false, "", fmt.Errorf("no test runner for ecosystem %q", ecosystem)
	}
	relFiles, err := runnableTestFiles(targetRoot, testDir)
	if err != nil {
		return false, "", err
	}
	if ecosystem == hyrum.EcoNPM && len(relFiles) == 0 {
		return false, "", fmt.Errorf("no runnable test files under %s", testDir)
	}
	argv := build(rel, relFiles)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = targetRoot
	out, runErr := cmd.CombinedOutput()
	return runErr == nil, string(out), nil
}

func runnableTestFiles(targetRoot, testDir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(testDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() || filepath.Ext(path) == ".json" {
			return nil
		}
		rel, err := filepath.Rel(targetRoot, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	return files, err
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
	hyrum.EcoPyPI:     "pip",
	hyrum.EcoGo:       "gomod",
	hyrum.EcoGem:      "bundler",
	hyrum.EcoCargo:    "cargo",
	hyrum.EcoComposer: "composer",
	hyrum.EcoHex:      "mix",
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

func versionOr(v, fallback string) string {
	if v != "" {
		return v
	}
	if fallback != "" {
		return fallback
	}
	return "installed"
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}
