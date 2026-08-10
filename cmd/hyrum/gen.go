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
		if err := stageContext(ctx, t, d, ws, rc); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: stage: %v\n", d.Name, err)
			continue
		}
		job := harness.Job{
			Workspace:  ws,
			SrcDir:     "target",
			SkillName:  "hyrum-generate",
			OutputFile: "tests.json",
		}
		if !*run {
			fmt.Printf("staged %s: %s %v\n", d.Name, h.Binary(), h.Args(job))
			continue
		}
		// TODO: exec + ParseStream + write tests to *out. See scrutineer
		// internal/worker/claude.go for the pattern.
		_ = out
		return notYet("gen --run")
	}
	return nil
}

// stageContext writes the per-dependency context bundle into ws:
//
//	ws/target/            symlink or copy of the target checkout
//	ws/dep/               shallow clone of the dependency's source repo
//	ws/dep-outline.md     outline.Pack of ws/dep
//	ws/usage.json         usage.Index of target against dep
//	ws/context.json       purl, versions, ecosystem
func stageContext(ctx context.Context, t *hyrum.Target, d hyrum.Dep, ws string, rc *registries.Client) error {
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return err
	}

	// Target: symlink so the skill sees the real tree without a copy.
	targetLink := filepath.Join(ws, "target")
	_ = os.Remove(targetLink)
	if err := os.Symlink(t.Path, targetLink); err != nil {
		return fmt.Errorf("link target: %w", err)
	}

	// Usage surface (works even without the dep clone).
	surf, err := usage.Index(d.Ecosystem, t.Path, d.Name)
	if err != nil {
		return fmt.Errorf("usage: %w", err)
	}
	surf.PURL = d.PURL
	if err := writeJSON(filepath.Join(ws, "usage.json"), surf); err != nil {
		return err
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
		return err
	}
	if repoURL == "" {
		return nil
	}
	depDir := filepath.Join(ws, "dep")
	if err := clone.Ensure(ctx, clone.Retry{}, repoURL, depDir, "", false); err != nil {
		return fmt.Errorf("clone %s: %w", repoURL, err)
	}
	res, err := outline.Pack(depDir, outline.Options{Compress: true})
	if err != nil {
		return fmt.Errorf("outline: %w", err)
	}
	f, err := os.Create(filepath.Join(ws, "dep-outline.md"))
	if err != nil {
		return err
	}
	defer f.Close()
	return res.Markdown(f)
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
