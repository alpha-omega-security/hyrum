package main

import (
	"context"
	"fmt"
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
func cmdCheck(args []string) error {
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
	// brief has already picked a package manager for this repo; hand that name
	// to managers.Detect so its ranking (which prefers bun over npm for a bare
	// package.json) does not override what the lockfile or config indicates.
	// brief titles some names ("Bun"); managers keys are lowercase.
	hint := ""
	for _, pm := range t.Report.PackageManagers {
		if pm.Lockfile != "" {
			if _, err := os.Stat(filepath.Join(t.Path, pm.Lockfile)); err == nil {
				hint = strings.ToLower(pm.Name)
				break
			}
		}
	}
	if hint == "" && len(t.Report.PackageManagers) > 0 {
		hint = strings.ToLower(t.Report.PackageManagers[0].Name)
	}
	mgr, err := detectManager(t.Path, hint)
	if err != nil {
		return fmt.Errorf("detect package manager: %w", err)
	}

	ctx := context.Background()
	testsRoot := outRoot(t.Path, *root)
	failed := false

	for _, spec := range deps {
		name, version := splitDepSpec(spec)
		d, _ := findDep(t, name)
		if d.Ecosystem == "" {
			d.Ecosystem = mgr.Ecosystem()
		}

		testDir := filepath.Join(testsRoot, name)
		if _, err := os.Stat(testDir); err != nil {
			fmt.Fprintf(os.Stderr, "%s: no tests at %s\n", name, testDir)
			continue
		}

		if version != "" {
			fmt.Fprintf(os.Stderr, "→ %s add %s@%s\n", mgr.Name(), name, version)
			if r, err := mgr.Add(ctx, name, managers.AddOptions{Version: version}); err != nil || !r.Success() {
				fmt.Fprintf(os.Stderr, "  install failed: %v %s\n", err, r.Stderr)
				failed = true
				continue
			}
		}

		ok, out, err := runTests(ctx, t.Path, testDir, d.Ecosystem)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		status := "PASS"
		if !ok {
			status = "FAIL"
			failed = true
		}
		fmt.Printf("%s@%s: %s\n", name, versionOr(version, d.Version), status)
		if !ok {
			fmt.Println(indent(out, "  "))
		}
	}
	if failed {
		return fmt.Errorf("one or more checks failed")
	}
	return nil
}

// testRunners maps a purl ecosystem type to the command that runs tests under
// a given directory. The command runs with cwd set to the target root so
// relative imports and node_modules resolution behave as they would in CI.
// The dir argument is a relative path; files is the pre-globbed list of test
// files under it for runners that need explicit paths.
var testRunners = map[string]func(dir string, files []string) []string{
	"npm":    func(_ string, files []string) []string { return append([]string{"node", "--test"}, files...) },
	"pypi":   func(dir string, _ []string) []string { return []string{"python3", "-m", "pytest", "-q", dir} },
	"golang": func(dir string, _ []string) []string { return []string{"go", "test", "./" + dir + "/..."} },
}

func runTests(ctx context.Context, targetRoot, testDir, ecosystem string) (ok bool, output string, err error) {
	rel, _ := filepath.Rel(targetRoot, testDir)
	build, known := testRunners[ecosystem]
	if !known {
		return false, "", fmt.Errorf("no test runner for ecosystem %q", ecosystem)
	}
	files, _ := filepath.Glob(filepath.Join(testDir, "*", "*"))
	relFiles := make([]string, 0, len(files))
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && !fi.IsDir() && filepath.Ext(f) != ".json" {
			r, _ := filepath.Rel(targetRoot, f)
			relFiles = append(relFiles, r)
		}
	}
	argv := build(rel, relFiles)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = targetRoot
	out, runErr := cmd.CombinedOutput()
	return runErr == nil, string(out), nil
}

func detectManager(dir, hint string) (managers.Manager, error) { //nolint:ireturn // registry lookup
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
