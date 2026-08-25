package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	hyrumconfig "github.com/alpha-omega-security/hyrum/internal/config"
	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/alpha-omega-security/hyrum/internal/hyrum/usage"
	"github.com/git-pkgs/purl"
)

// cmdSurface prints the usage surface of the target's dependencies. With no
// --dep it prints a summary row per direct dependency; with --dep it prints
// the full symbol/site list for that dependency.
func cmdSurface(ctx context.Context, args []string) error {
	fs := newFlags("surface")
	var deps, symbols, scopes, includes, excludes stringList
	fs.Var(&deps, "dep", "dependency name to detail (repeatable); default: summarise all direct deps")
	fs.Var(&symbols, "symbol", "exact dependency symbol to include (repeatable; requires one --dep)")
	fs.Var(&scopes, "scope", "usage scope to include: production, test, example, or documentation (repeatable); default: all")
	fs.Var(&includes, "include", "relative path prefix to include (repeatable)")
	fs.Var(&excludes, "exclude", "relative path prefix to exclude (repeatable)")
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")
	directOnly := fs.Bool("direct", true, "restrict summary to direct dependencies")
	configPath := fs.String("config", "", "configuration file (default: <target>/hyrum.yaml when present)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(symbols) > 0 && len(deps) != 1 {
		return fmt.Errorf("--symbol requires exactly one --dep")
	}
	set := visitedFlags(fs)
	if set["config"] && *configPath == "" {
		return fmt.Errorf("--config requires a non-empty path")
	}
	opts, err := resolveUsageOptions(scopes, includes, excludes, nil)
	if err != nil {
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
	cfg, _, err := hyrumconfig.Load(t.Path, *configPath)
	if err != nil {
		return err
	}
	configuredDeps := append([]hyrum.Dep(nil), t.Deps...)
	for _, name := range deps {
		if dep, ok := findDep(t, name); ok && !containsDepPURL(configuredDeps, dep.PURL) {
			configuredDeps = append(configuredDeps, dep)
		}
	}
	opts = withConfiguredActivations(opts, configuredDeps, cfg.Deps)

	if len(deps) == 0 {
		return surfaceSummaryWithOptions(ctx, t, *directOnly, *asJSON, opts)
	}
	return surfaceDetailWithOptions(ctx, t, deps, symbols, *asJSON, opts)
}

func surfaceSummaryWithOptions(
	ctx context.Context,
	t *hyrum.Target,
	directOnly, asJSON bool,
	opts usage.IndexOptions,
) error {
	type row struct {
		Name            string `json:"name"`
		Ecosystem       string `json:"ecosystem"`
		Version         string `json:"version"`
		Scope           string `json:"scope"`
		Symbols         int    `json:"symbols"`
		Sites           int    `json:"sites"`
		ProductionSites int    `json:"production_sites"`
		TestSites       int    `json:"test_sites"`
		OtherSites      int    `json:"other_sites"`
		Indexer         string `json:"indexer"`
	}
	var deps []hyrum.Dep
	var depPURLs []string
	for _, d := range t.Deps {
		if directOnly && !d.Direct {
			continue
		}
		deps = append(deps, d)
		depPURLs = append(depPURLs, d.PURL)
	}
	var indexed map[string]usage.IndexResult
	var err error
	if emptyUsageOptions(opts) {
		indexed, err = usage.IndexMany(ctx, t.Path, depPURLs)
	} else {
		indexed, err = usage.IndexManyWithOptions(ctx, t.Path, depPURLs, opts)
	}
	if err != nil {
		return err
	}

	var rows []row
	for _, d := range deps {
		r := row{Name: d.Name, Ecosystem: d.Ecosystem, Version: d.Version, Scope: d.Scope}
		result, ok := indexed[d.PURL]
		if ok && result.Err == nil && result.Surface != nil {
			s := result.Surface
			r.Symbols = s.UsedCount()
			for _, sym := range s.Symbols {
				for _, site := range sym.Sites {
					r.Sites++
					switch site.Scope {
					case usage.ScopeProduction:
						r.ProductionSites++
					case usage.ScopeTest:
						r.TestSites++
					default:
						r.OtherSites++
					}
				}
			}
			r.Indexer = "ok"
		} else {
			r.Indexer = "unsupported"
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Sites > rows[j].Sites })

	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(rows)
	}
	const columnGap = 2
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, columnGap, ' ', 0)
	fmt.Fprintln(tw, "DEP\tECOSYSTEM\tVERSION\tSCOPE\tSYMBOLS\tSITES\tPROD\tTEST\tOTHER\tINDEX")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n",
			r.Name, r.Ecosystem, r.Version, r.Scope, r.Symbols, r.Sites,
			r.ProductionSites, r.TestSites, r.OtherSites, r.Indexer)
	}
	return tw.Flush()
}

func surfaceDetailWithOptions(
	ctx context.Context,
	t *hyrum.Target,
	names []string,
	symbols []string,
	asJSON bool,
	opts usage.IndexOptions,
) error {
	var out []*usage.Surface
	for _, name := range names {
		d, ok := findDep(t, name)
		if !ok {
			return fmt.Errorf("dependency %q not found in %s (ecosystems: %v)", name, t.Path, t.Ecosystems())
		}
		var s *usage.Surface
		var err error
		if emptyUsageOptions(opts) {
			s, err = usage.Index(ctx, t.Path, d.PURL)
		} else {
			s, err = usage.IndexWithOptions(ctx, t.Path, d.PURL, opts)
		}
		if err != nil {
			return err
		}
		s, err = selectUsageSymbols(s, symbols)
		if err != nil {
			return fmt.Errorf("%s: %w", d.Name, err)
		}
		out = append(out, s)
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	const contextWidth = 80
	for _, s := range out {
		fmt.Printf("# %s (%s) — %d symbols\n", s.Dep, s.Ecosystem, s.UsedCount())
		for _, sym := range s.Symbols {
			fmt.Printf("  %s  [%s]  %d site(s)\n", sym.Name, sym.Kind, len(sym.Sites))
			for _, site := range sym.Sites {
				fmt.Printf("    %s:%d  [%s]  %s\n", site.File, site.Line, site.Scope, truncate(site.Context, contextWidth))
			}
		}
	}
	return nil
}

func emptyUsageOptions(opts usage.IndexOptions) bool {
	return len(opts.Scopes) == 0 && len(opts.IncludePaths) == 0 &&
		len(opts.ExcludePaths) == 0 && len(opts.Activations) == 0
}

func containsDepPURL(deps []hyrum.Dep, purl string) bool {
	for _, dep := range deps {
		if dep.PURL == purl {
			return true
		}
	}
	return false
}

func findDep(t *hyrum.Target, name string) (hyrum.Dep, bool) {
	for _, d := range t.Deps {
		if d.Name == name || d.PURL == name {
			return d, true
		}
	}
	// Not in the manifest. A directly-imported transitive (httpbin importing
	// werkzeug via Flask) is exactly the Hyrum's Law case, so allow --dep to
	// name any package and try each detected ecosystem's indexer.
	for _, eco := range t.Ecosystems() {
		if usage.Supported(eco) {
			return hyrum.Dep{
				Name: name, Ecosystem: eco, Direct: false,
				PURL: purl.New(eco, "", name, "", nil).String(),
			}, true
		}
	}
	return hyrum.Dep{}, false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
