package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

	var corpusErrs []error
	for _, spec := range specs {
		display := clone.RedactURL(spec)
		url, ref := splitDependentSpec(spec)
		fmt.Fprintf(os.Stderr, "▶ dependent %s\n", display)
		dir := filepath.Join(*work, "targets", slugify(url))
		if err := clone.Ensure(ctx, clone.Retry{}, url, dir, ref, true); err != nil {
			fmt.Fprintf(os.Stderr, "  clone: %v\n", err)
			corpusErrs = append(corpusErrs, fmt.Errorf("%s: clone: %w", display, err))
			continue
		}
		// corpus owns this clone (unlike gen's symlinked target), so agent
		// directive files can be removed before the skill run reads it.
		if n, err := harness.StripDirectives(dir); err != nil {
			fmt.Fprintf(os.Stderr, "  strip: %v\n", err)
			corpusErrs = append(corpusErrs, fmt.Errorf("%s: strip: %w", display, err))
			continue
		} else if n > 0 {
			fmt.Fprintf(os.Stderr, "  stripped %d agent directive path(s)\n", n)
		}
		t, err := hyrum.Analyze(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  analyze: %v\n", err)
			corpusErrs = append(corpusErrs, fmt.Errorf("%s: analyze: %w", display, err))
			continue
		}
		d, ok := findDep(t, *upstream)
		if !ok {
			fmt.Fprintf(os.Stderr, "  %s does not use %s; skipping\n", url, *upstream)
			corpusErrs = append(corpusErrs, fmt.Errorf("%s: does not use %s", display, *upstream))
			continue
		}
		if err := p.genAll(ctx, t, []hyrum.Dep{d}); err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			corpusErrs = append(corpusErrs, fmt.Errorf("%s: generate: %w", display, err))
		}
	}
	return errors.Join(corpusErrs...)
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
	const poolFactor, minPool = 10, 50
	pool := max(n*poolFactor, minPool)
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
	if i := lastAt(s); i > 0 && i > refSeparatorBoundary(s) {
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

// refSeparatorBoundary returns the earliest position at which an @ can denote
// a ref. For scheme URLs this is the start of the path, after any userinfo in
// the authority. For scp-like Git URLs it is after the git@ user prefix.
func refSeparatorBoundary(s string) int {
	const scheme, sshUser = "://", "git@"
	if i := strings.Index(s, scheme); i >= 0 {
		authority := i + len(scheme)
		if path := strings.IndexByte(s[authority:], '/'); path >= 0 {
			return authority + path
		}
		return len(s)
	}
	if strings.HasPrefix(s, sshUser) {
		return len(sshUser)
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
