package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alpha-omega-security/harness"
	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/git-pkgs/clone"
	"github.com/git-pkgs/dependents"
	"github.com/git-pkgs/enrichment"
	"github.com/git-pkgs/registries"
)

// cmdCorpus generates Hyrum's tests for one upstream dependency from the
// perspective of several dependent repositories, aggregating the output into
// a single tests/hyrum/<upstream>/from_<dependent>/ tree that the upstream's
// maintainers can run as one suite.
func cmdCorpus(ctx context.Context, args []string) error {
	fs := newFlags("corpus")
	upstream := fs.String("upstream", "", "upstream dependency name as it appears in dependents' manifests (required)")
	upstreamRepo := fs.String("upstream-repo", "", "upstream repository URL for --discover (default: registry lookup on --upstream purl)")
	var explicitDeps stringList
	fs.Var(&explicitDeps, "dependent", "dependent repository URL (repeatable)")
	discoverN := fs.Int("discover", 0, "auto-discover N dependents via ecosyste.ms (git-pkgs/dependents)")
	out := fs.String("out", "", "output directory for the aggregated corpus (required)")
	work := fs.String("work", filepath.Join(os.TempDir(), "hyrum-corpus"), "working directory for clones and skill workspaces")
	backend := fs.String("backend", "claude", "harness backend: "+harness.Names())
	run := fs.Bool("run", false, "invoke the backend (otherwise stage only)")
	container := fs.String("container", "", "run the backend in a container using this image (\"default\" for the published runner)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *upstream == "" {
		return fmt.Errorf("--upstream is required")
	}
	if *out == "" {
		return fmt.Errorf("--out is required")
	}

	h, err := harness.ByName(*backend)
	if err != nil {
		return err
	}
	p := newPipeline(h, *work, *out, *run, resolveContainer(*container))
	rc := p.rc

	specs := []string(explicitDeps)
	if *discoverN > 0 {
		found, err := discoverDependents(ctx, rc, *upstream, *upstreamRepo, *discoverN)
		if err != nil {
			return fmt.Errorf("discover: %w", err)
		}
		specs = append(specs, found...)
	}
	if len(specs) == 0 {
		return fmt.Errorf("no dependents: pass --dependent or --discover N")
	}

	for _, spec := range specs {
		url, ref := splitDependentSpec(spec)
		fmt.Fprintf(os.Stderr, "▶ dependent %s\n", spec)
		dir := filepath.Join(*work, "targets", slugify(url))
		if err := clone.Ensure(ctx, clone.Retry{}, url, dir, ref, true); err != nil {
			fmt.Fprintf(os.Stderr, "  clone: %v\n", err)
			continue
		}
		t, err := hyrum.Analyze(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  analyze: %v\n", err)
			continue
		}
		d, ok := findDep(t, *upstream)
		if !ok {
			fmt.Fprintf(os.Stderr, "  %s does not use %s; skipping\n", url, *upstream)
			continue
		}
		if err := p.genAll(ctx, t, []hyrum.Dep{d}); err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
		}
	}
	return nil
}

// discoverDependents queries ecosyste.ms via git-pkgs/dependents for the
// top-N repositories that depend on upstream, filtered to non-forks with a
// repository URL, sorted by download count.
func discoverDependents(ctx context.Context, rc *registries.Client, upstream, repo string, n int) ([]string, error) {
	if repo == "" {
		pkg, err := registries.FetchPackageFromPURL(ctx, upstream, rc)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w (pass --upstream-repo)", upstream, err)
		}
		repo = pkg.Repository
	}
	if repo == "" {
		return nil, fmt.Errorf("no repository URL for %s (pass --upstream-repo)", upstream)
	}
	ec, err := enrichment.NewEcosystemsClient()
	if err != nil {
		return nil, err
	}
	// Over-fetch so filtering and ranking have a pool to work with;
	// ecosyste.ms does not return dependents pre-sorted by popularity.
	pool := n * 10
	if pool < 50 {
		pool = 50
	}
	cands, err := dependents.DiscoverRepository(ctx, ec, repo, dependents.DiscoverOptions{
		MaxDependentsPerPackage: pool,
	})
	if err != nil {
		return nil, err
	}
	kept, _ := dependents.Filter(cands, dependents.FilterOptions{
		ExcludeForks:    true,
		ExcludeArchived: true,
		ExcludeMirrors:  true,
	})
	ranked := dependents.Rank(kept, 0, dependents.PopularityScore)
	var out []string
	for _, c := range ranked {
		if c.Repository == "" {
			continue
		}
		out = append(out, c.Repository)
		fmt.Fprintf(os.Stderr, "  discovered %s (score %d)\n", c.Repository, dependents.PopularityScore(c))
		if len(out) >= n {
			break
		}
	}
	return out, nil
}

// splitDependentSpec parses "url" or "url@ref". The @ is taken from the end
// so ssh URLs (git@host:path) are left intact when no ref is given.
func splitDependentSpec(s string) (url, ref string) {
	if i := lastAt(s); i > 0 && i > indexOfScheme(s) {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func lastAt(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '@' {
			return i
		}
	}
	return -1
}

// indexOfScheme returns the position after "://" or after "git@", so an @ in
// the auth/scheme portion is not mistaken for a ref separator.
func indexOfScheme(s string) int {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == ':' && s[i+1] == '/' && s[i+2] == '/' {
			return i + 3
		}
	}
	if len(s) > 4 && s[:4] == "git@" {
		return 4
	}
	return 0
}

func slugify(url string) string {
	s := url
	for _, pre := range []string{"https://", "http://", "git@"} {
		if len(s) > len(pre) && s[:len(pre)] == pre {
			s = s[len(pre):]
		}
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '/' || c == ':' || c == '.' {
			c = '_'
		}
		out = append(out, c)
	}
	return string(out)
}
