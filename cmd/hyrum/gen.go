package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alpha-omega-security/harness"
	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/alpha-omega-security/hyrum/internal/hyrum/usage"
	"github.com/git-pkgs/clone"
	"github.com/git-pkgs/outline"
	"github.com/git-pkgs/registries"
)

// cmdGen runs the generation pipeline for one or more dependencies. In the
// spike this assembles the context bundle (usage surface + dep outline) and
// stages a harness.Job for the hyrum-generate skill; actually invoking the
// backend is gated behind --run so the wiring can be exercised without
// spending tokens.
func cmdGen(args []string) error {
	fs := newFlags("gen")
	var deps stringList
	fs.Var(&deps, "dep", "dependency name to generate for (repeatable); default: all direct runtime deps")
	out := fs.String("out", "tests/hyrum", "output root for generated tests")
	work := fs.String("work", filepath.Join(os.TempDir(), "hyrum"), "working directory for clones and skill workspaces")
	backend := fs.String("backend", "claude", "harness backend: "+harness.Names())
	run := fs.Bool("run", false, "actually invoke the backend (otherwise stage only)")
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

	ctx := context.Background()
	rc := registries.DefaultClient()

	for _, d := range selected {
		ws := filepath.Join(*work, d.Ecosystem, d.Name)
		depDir, err := stageContext(ctx, t, d, ws, rc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s: stage: %v\n", d.Name, err)
			continue
		}
		if err := hyrum.GatherHistory(ctx, t, d, depDir, ws); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: history: %v\n", d.Name, err)
		}
		if !*run {
			job := harness.Job{Workspace: ws, SrcDir: "target", SkillName: "hyrum-generate", OutputFile: "tests.json"}
			fmt.Printf("staged %s: %s %v\n", d.Name, h.Binary(), h.Args(job))
			continue
		}

		fmt.Fprintf(os.Stderr, "→ %s (%s)\n", d.Name, d.PURL)
		steps := []struct{ skill, out string }{
			{"hyrum-usage", "surface.json"},
			{"hyrum-history", "breaks.json"},
			{"hyrum-generate", "tests.json"},
		}
		var res *hyrum.RunResult
		var totalCost float64
		for _, s := range steps {
			fmt.Fprintf(os.Stderr, "  [%s]\n", s.skill)
			r, err := hyrum.RunSkill(ctx, h, ws, s.skill, s.out)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s/%s: %v\n", d.Name, s.skill, err)
				// usage and history are enrichment; generate is required.
				if s.skill == "hyrum-generate" {
					res = nil
				}
				continue
			}
			totalCost += r.CostUSD
			res = r
		}
		if res == nil {
			continue
		}

		outDir := filepath.Join(t.Path, *out, d.Name, "from_"+targetName(t))
		written, err := hyrum.WriteFiles(outDir, res.Output.Files)
		if err != nil {
			return fmt.Errorf("%s: write: %w", d.Name, err)
		}
		if err := writeJSON(filepath.Join(outDir, "meta.json"), map[string]any{
			"purl":       d.PURL,
			"ecosystem":  d.Ecosystem,
			"baseline":   d.Version,
			"session_id": res.SessionID,
			"cost_usd":   totalCost,
			"notes":      res.Output.Notes,
		}); err != nil {
			return err
		}
		fmt.Printf("%s: %d file(s) → %s ($%.4f)\n", d.Name, len(written), outDir, totalCost)
	}
	return nil
}

// targetName is the from_<x> directory component. Uses brief's detected git
// remote basename when available, else the path basename.
func targetName(t *hyrum.Target) string {
	if t.Report.Git != nil {
		for _, url := range t.Report.Git.Remotes {
			base := filepath.Base(url)
			if i := len(base) - len(".git"); i > 0 && base[i:] == ".git" {
				base = base[:i]
			}
			if base != "" {
				return base
			}
		}
	}
	return filepath.Base(t.Path)
}

// stageContext writes the per-dependency context bundle into ws:
//
//	ws/target/            symlink or copy of the target checkout
//	ws/dep/               shallow clone of the dependency's source repo
//	ws/dep-outline.md     outline.Pack of ws/dep
//	ws/usage.json         usage.Index of target against dep
//	ws/context.json       purl, versions, ecosystem
//
// Returns the dep clone directory (empty when no repo URL was found) so
// callers can pass it to GatherHistory for changelog discovery.
func stageContext(ctx context.Context, t *hyrum.Target, d hyrum.Dep, ws string, rc *registries.Client) (string, error) {
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return "", err
	}

	// Target: symlink so the skill sees the real tree without a copy.
	targetLink := filepath.Join(ws, "target")
	_ = os.Remove(targetLink)
	if err := os.Symlink(t.Path, targetLink); err != nil {
		return "", fmt.Errorf("link target: %w", err)
	}

	// Usage surface (works even without the dep clone).
	surf, err := usage.Index(d.Ecosystem, t.Path, d.Name)
	if err != nil {
		return "", fmt.Errorf("usage: %w", err)
	}
	surf.PURL = d.PURL
	if err := writeJSON(filepath.Join(ws, "usage.json"), surf); err != nil {
		return "", err
	}

	// Dep source + outline: best-effort. Registries gives the repo URL;
	// clone.Ensure gives a shallow checkout; outline.Pack reduces it.
	repoURL, latest, rerr := lookupRepo(ctx, rc, d)
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
		return "", err
	}
	if repoURL == "" {
		return "", nil
	}
	depDir := filepath.Join(ws, "dep")
	// Full clone: hyrum-history diffs between version tags and reads
	// History.md at old refs, which a shallow clone cannot serve. The dep
	// clone is reused across runs via Ensure so the cost is one-time.
	if err := clone.Ensure(ctx, clone.Retry{}, repoURL, depDir, "", true); err != nil {
		return "", fmt.Errorf("clone %s: %w", repoURL, err)
	}
	res, err := outline.Pack(depDir, outline.Options{Compress: true})
	if err != nil {
		return depDir, fmt.Errorf("outline: %w", err)
	}
	f, err := os.Create(filepath.Join(ws, "dep-outline.md"))
	if err != nil {
		return depDir, err
	}
	defer f.Close()
	return depDir, res.Markdown(f)
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
