package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alpha-omega-security/harness"
	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/alpha-omega-security/hyrum/internal/hyrum/usage"
	"github.com/git-pkgs/clone"
	"github.com/git-pkgs/outline"
	"github.com/git-pkgs/registries"
	_ "github.com/git-pkgs/registries/all" // register every ecosystem's init()
	"github.com/git-pkgs/vers"
)

// cmdGen runs the generation pipeline for one or more dependencies: stage the
// context bundle, gather history inputs, then run the usage/history/generate
// skills in sequence. Without --run it stops after staging so the workspace
// can be inspected without invoking a backend.
func cmdGen(ctx context.Context, args []string) error {
	fs := newFlags("gen")
	var deps stringList
	fs.Var(&deps, "dep", "dependency name to generate for (repeatable); default: all direct runtime deps")
	out := fs.String("out", "tests/hyrum", "output root for generated tests")
	work := fs.String("work", filepath.Join(os.TempDir(), "hyrum"), "working directory for clones and skill workspaces")
	backend := fs.String("backend", "claude", "harness backend: "+harness.Names())
	run := fs.Bool("run", false, "actually invoke the backend (otherwise stage only)")
	container := fs.String("container", "", "run the backend in a container using this image (\"default\" for "+hyrum.DefaultRunnerImage+")")
	verify := fs.Bool("verify", false, "after generating, run the tests against the baseline and latest dep versions and record results in meta.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := "."
	if fs.NArg() > 0 {
		path = fs.Arg(0)
	}

	t, err := hyrum.Analyze(path)
	if err != nil {
		return err
	}

	selected := selectDeps(t, deps)
	if len(selected) == 0 {
		return fmt.Errorf("no dependencies selected (target has %d direct deps across %v)", len(t.Deps), t.Ecosystems())
	}

	h, err := harness.ByName(*backend)
	if err != nil {
		return err
	}

	p := newPipeline(h, *work, outRoot(t.Path, *out), *run, resolveContainer(*container))
	p.verify = *verify
	return p.genAll(ctx, t, selected)
}

// pipeline holds the shared configuration for running the stage → history →
// skills → write sequence over one or more dependencies of one target.
// cmdGen and cmdCorpus both drive it.
type pipeline struct {
	h       harness.Harness
	rc      *registries.Client
	runner  hyrum.Runner
	work    string
	outRoot string
	run     bool
	// containerImage non-empty means the runner is a ContainerRunner and each
	// genOne call sets its TargetPath to the analysed target so /work/target
	// is a read-only bind mount instead of a host symlink.
	containerImage string
	// verify runs the generated tests against baseline and latest after
	// writing them and records results in meta.json.
	verify bool
}

// newPipeline builds a pipeline from the flags gen and corpus share.
func newPipeline(h harness.Harness, work, outRoot string, run bool, containerImage string) *pipeline {
	p := &pipeline{
		h:              h,
		rc:             registries.DefaultClient(),
		work:           work,
		outRoot:        outRoot,
		run:            run,
		containerImage: containerImage,
	}
	if containerImage == "" {
		p.runner = hyrum.HostRunner{}
	}
	// ContainerRunner is constructed per genOne with the target path.
	return p
}

// genAll builds the target's history index once and runs genOne for each dep.
func (p *pipeline) genAll(ctx context.Context, t *hyrum.Target, deps []hyrum.Dep) error {
	idx, err := hyrum.BuildHistoryIndex(ctx, t, deps)
	if err != nil {
		return fmt.Errorf("history index: %w", err)
	}
	for _, d := range deps {
		if err := p.genOne(ctx, t, idx, d); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", d.Name, err)
		}
	}
	return nil
}

func (p *pipeline) genOne(ctx context.Context, t *hyrum.Target, idx *hyrum.HistoryIndex, d hyrum.Dep) error {
	ws := filepath.Join(p.work, targetName(t), d.Ecosystem, d.Name)
	depDir, latest, err := stageContext(ctx, t, d, ws, p.rc)
	if err != nil {
		return fmt.Errorf("stage: %w", err)
	}
	if err := hyrum.GatherHistory(ctx, idx, d, depDir, latest, ws); err != nil {
		fmt.Fprintf(os.Stderr, "  %s: history: %v\n", d.Name, err)
	}
	if !p.run {
		job := harness.Job{Workspace: ws, SrcDir: "target", SkillName: "hyrum-generate", OutputFile: "tests.json"}
		fmt.Printf("staged %s: %s %v\n", d.Name, p.h.Binary(), p.h.Args(job))
		return nil
	}

	fmt.Fprintf(os.Stderr, "→ %s ← %s (%s)\n", d.Name, targetName(t), d.PURL)
	runner := p.runner
	if runner == nil {
		runner = hyrum.ContainerRunner{Image: p.containerImage, TargetPath: t.Path}
	}
	steps := []struct{ skill, out string }{
		{"hyrum-usage", "surface.json"},
		{"hyrum-history", "breaks.json"},
		{"hyrum-generate", "tests.json"},
	}
	var res *hyrum.RunResult
	var totalCost float64
	for _, s := range steps {
		fmt.Fprintf(os.Stderr, "  [%s]\n", s.skill)
		r, err := runner.RunSkill(ctx, p.h, ws, s.skill, s.out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s/%s: %v\n", d.Name, s.skill, err)
			if s.skill == "hyrum-generate" {
				return err
			}
			continue
		}
		totalCost += r.CostUSD
		res = r
	}
	if res == nil {
		return fmt.Errorf("generate produced no output")
	}

	outDir := filepath.Join(p.outRoot, d.Name, "from_"+targetName(t))
	written, err := hyrum.WriteFiles(outDir, res.Output.Files)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	meta := map[string]any{
		"purl":       d.PURL,
		"ecosystem":  d.Ecosystem,
		"baseline":   d.Version,
		"target":     targetName(t),
		"session_id": res.SessionID,
		"cost_usd":   totalCost,
		"notes":      res.Output.Notes,
	}
	if p.verify {
		meta["verify"] = p.runVerify(ctx, ws, d, latest, res.Output.Files)
	}
	if err := writeJSON(filepath.Join(outDir, "meta.json"), meta); err != nil {
		return err
	}
	fmt.Printf("%s ← %s: %d file(s) → %s ($%.4f)\n", d.Name, targetName(t), len(written), outDir, totalCost)
	return nil
}

// runVerify installs the dep at baseline and latest in a scratch dir under
// the workspace and runs the generated tests against each. The scratch dir is
// separate from the target so the user's checkout and lockfile are untouched.
func (p *pipeline) runVerify(ctx context.Context, ws string, d hyrum.Dep, latest string, files []hyrum.GeneratedFile) []hyrum.VerifyResult {
	tc, ok := testRunners[d.Ecosystem]
	if !ok {
		return []hyrum.VerifyResult{{Error: "no test runner for ecosystem " + d.Ecosystem}}
	}
	scratch := filepath.Join(ws, "verify")
	_ = os.RemoveAll(scratch)
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return []hyrum.VerifyResult{{Error: err.Error()}}
	}
	mgr, err := detectManagerFor(scratch, d.Ecosystem)
	if err != nil {
		return []hyrum.VerifyResult{{Error: fmt.Sprintf("manager for %s: %v", d.Ecosystem, err)}}
	}
	versions := []string{constraintVersion(d.Version, d.Ecosystem), latest}
	fmt.Fprintf(os.Stderr, "  [verify] %s at %v\n", d.Name, versions)
	results := hyrum.VerifyMatrix(ctx, mgr, hyrum.TestCommand(tc), scratch, d.Name, files, versions)
	for _, r := range results {
		if r.Error != "" {
			fmt.Fprintf(os.Stderr, "    %s: error: %s\n", r.Version, r.Error)
		} else {
			fmt.Fprintf(os.Stderr, "    %s: %d pass, %d fail %v\n", r.Version, r.Pass, r.Fail, r.Failed)
		}
	}
	return results
}

// constraintVersion returns an installable version from a manifest constraint
// under the given ecosystem's native syntax (^/~ for npm, ~> for gem, ~= for
// pypi, ...). The returned version is the range's inclusive lower bound;
// upper-bound-only or wildcard constraints return "".
func constraintVersion(v, ecosystem string) string {
	if v == "" {
		return ""
	}
	r, err := (&vers.Parser{}).ParseNative(v, ecosystem)
	if err != nil || len(r.Intervals) == 0 {
		return ""
	}
	iv := r.Intervals[0]
	if iv.Min == "" || !iv.MinInclusive {
		return ""
	}
	return iv.Min
}

// resolveContainer maps the --container flag value to an image name. Empty
// means host mode; "default" selects the published runner image.
func resolveContainer(v string) string {
	if v == "default" {
		return hyrum.DefaultRunnerImage
	}
	return v
}

// outRoot resolves the --out flag: absolute paths are used as-is; relative
// paths are joined onto the target so tests/hyrum lands next to the code that
// was analysed.
func outRoot(targetPath, out string) string {
	if filepath.IsAbs(out) {
		return out
	}
	return filepath.Join(targetPath, out)
}

// targetName is the from_<x> directory component. Prefers the basename of the
// origin remote, then any https remote, then the path basename. brief's
// Remotes is a map so plain iteration would pick a deploy remote (dokku,
// heroku) at random.
func targetName(t *hyrum.Target) string {
	if t.Report.Git != nil {
		remotes := t.Report.Git.Remotes
		if u := remotes["origin"]; u != "" {
			return remoteBasename(u)
		}
		for _, u := range remotes {
			if strings.HasPrefix(u, "https://") {
				return remoteBasename(u)
			}
		}
	}
	return filepath.Base(t.Path)
}

func remoteBasename(url string) string {
	base := filepath.Base(url)
	if i := len(base) - len(".git"); i > 0 && base[i:] == ".git" {
		base = base[:i]
	}
	return base
}

// stageContext writes the per-dependency context bundle into ws:
//
//	ws/target/            symlink or copy of the target checkout
//	ws/dep/               shallow clone of the dependency's source repo
//	ws/dep-outline.md     outline.Pack of ws/dep
//	ws/usage.json         usage.Index of target against dep
//	ws/context.json       purl, versions, ecosystem
//
// Returns the dep clone directory (empty when no repo URL was found) and the
// dep's latest version from the registry, so callers can pass both to
// GatherHistory for changelog discovery and range slicing.
func stageContext(ctx context.Context, t *hyrum.Target, d hyrum.Dep, ws string, rc *registries.Client) (depDir, latest string, err error) {
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return "", "", err
	}

	// Target: symlink so the skill sees the real tree without a copy.
	targetLink := filepath.Join(ws, "target")
	_ = os.Remove(targetLink)
	if err := os.Symlink(t.Path, targetLink); err != nil {
		return "", "", fmt.Errorf("link target: %w", err)
	}

	// Usage surface (works even without the dep clone).
	surf, err := usage.Index(d.Ecosystem, t.Path, d.Name)
	if err != nil {
		return "", "", fmt.Errorf("usage: %w", err)
	}
	surf.PURL = d.PURL
	if err := writeJSON(filepath.Join(ws, "usage.json"), surf); err != nil {
		return "", "", err
	}

	// Dep source + outline: best-effort. Registries gives the repo URL;
	// clone.Ensure gives a shallow checkout; outline.Pack reduces it.
	var repoURL string
	var rerr error
	repoURL, latest, rerr = lookupRepo(ctx, rc, d)
	meta := map[string]any{
		"purl":      d.PURL,
		"name":      d.Name,
		"ecosystem": d.Ecosystem,
		"version":   d.Version,
		"repo":      repoURL,
		"latest":    latest,
	}
	if rerr != nil {
		meta["registry_error"] = rerr.Error()
	}
	if err := writeJSON(filepath.Join(ws, "context.json"), meta); err != nil {
		return "", latest, err
	}
	if repoURL == "" {
		return "", latest, nil
	}
	depDir = filepath.Join(ws, "dep")
	// Full clone: hyrum-history diffs between version tags and reads
	// History.md at old refs, which a shallow clone cannot serve. The dep
	// clone is reused across runs via Ensure so the cost is one-time.
	// The dep clone and everything derived from it is best-effort: a bad
	// repository URL from the registry (or none) means the skills run with
	// usage.json and git-log.txt only.
	if err := clone.Ensure(ctx, clone.Retry{}, repoURL, depDir, "", true); err != nil {
		fmt.Fprintf(os.Stderr, "  clone %s: %v (continuing without dep source)\n", repoURL, err)
		return "", latest, nil
	}
	// The dep clone is ours to modify; remove any CLAUDE.md/AGENTS.md/.claude
	// so the cloned repository cannot inject instructions into the skill run.
	if _, err := hyrum.StripAgentDirectives(depDir); err != nil {
		return depDir, latest, fmt.Errorf("strip %s: %w", depDir, err)
	}
	res, err := outline.Pack(depDir, outline.Options{Compress: true})
	if err != nil {
		return depDir, latest, fmt.Errorf("outline: %w", err)
	}
	meta["exported_symbols"] = countExported(res)
	if err := writeJSON(filepath.Join(ws, "context.json"), meta); err != nil {
		return depDir, latest, err
	}
	f, err := os.Create(filepath.Join(ws, "dep-outline.md"))
	if err != nil {
		return depDir, latest, err
	}
	defer f.Close()
	return depDir, latest, res.Markdown(f)
}

// countExported returns the number of exported top-level declarations across
// all outlined files. Test files and files under obvious test directories are
// excluded so the count reflects the shipped surface.
func countExported(r *outline.Result) int {
	n := 0
	for _, f := range r.Files {
		if isTestPath(f.Path) {
			continue
		}
		for _, s := range f.Symbols {
			if s.Exported {
				n++
			}
		}
	}
	return n
}

func isTestPath(p string) bool {
	if strings.HasSuffix(p, "_test.go") {
		return true
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "test", "tests", "spec", "specs", "__tests__":
			return true
		}
	}
	return false
}

func lookupRepo(ctx context.Context, rc *registries.Client, d hyrum.Dep) (repoURL, latest string, err error) {
	if d.PURL == "" {
		return "", "", fmt.Errorf("no purl for %s", d.Name)
	}
	pkg, err := registries.FetchPackageFromPURL(ctx, d.PURL, rc)
	if err != nil {
		return "", "", err
	}
	return pkg.Repository, pkg.LatestVersion, nil
}

func selectDeps(t *hyrum.Target, names stringList) []hyrum.Dep {
	if len(names) == 0 {
		var out []hyrum.Dep
		for _, d := range t.Deps {
			if d.Direct && d.Scope != "development" && d.Scope != "dev" {
				out = append(out, d)
			}
		}
		return out
	}
	var out []hyrum.Dep
	for _, n := range names {
		if d, ok := findDep(t, n); ok {
			out = append(out, d)
		}
	}
	return out
}

func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
