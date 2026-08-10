package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/alpha-omega-security/hyrum/internal/hyrum/usage"
)

// cmdSurface prints the usage surface of the target's dependencies. With no
// --dep it prints a summary row per direct dependency; with --dep it prints
// the full symbol/site list for that dependency.
func cmdSurface(_ context.Context, args []string) error {
	fs := newFlags("surface")
	var deps stringList
	fs.Var(&deps, "dep", "dependency name to detail (repeatable); default: summarise all direct deps")
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")
	directOnly := fs.Bool("direct", true, "restrict summary to direct dependencies")
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

	if len(deps) == 0 {
		return surfaceSummary(t, *directOnly, *asJSON)
	}
	return surfaceDetail(t, deps, *asJSON)
}

func surfaceSummary(t *hyrum.Target, directOnly, asJSON bool) error {
	type row struct {
		Name      string `json:"name"`
		Ecosystem string `json:"ecosystem"`
		Version   string `json:"version"`
		Scope     string `json:"scope"`
		Symbols   int    `json:"symbols"`
		Sites     int    `json:"sites"`
		Indexer   string `json:"indexer"`
	}
	var rows []row
	for _, d := range t.Deps {
		if directOnly && !d.Direct {
			continue
		}
		r := row{Name: d.Name, Ecosystem: d.Ecosystem, Version: d.Version, Scope: d.Scope}
		if s, err := usage.Index(d.Ecosystem, t.Path, d.Name); err == nil {
			r.Symbols = s.UsedCount()
			for _, sym := range s.Symbols {
				r.Sites += len(sym.Sites)
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
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DEP\tECOSYSTEM\tVERSION\tSCOPE\tSYMBOLS\tSITES\tINDEX")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n",
			r.Name, r.Ecosystem, r.Version, r.Scope, r.Symbols, r.Sites, r.Indexer)
	}
	return tw.Flush()
}

func surfaceDetail(t *hyrum.Target, names []string, asJSON bool) error {
	var out []*usage.Surface
	for _, name := range names {
		d, ok := findDep(t, name)
		if !ok {
			return fmt.Errorf("dependency %q not found in %s (ecosystems: %v)", name, t.Path, t.Ecosystems())
		}
		s, err := usage.Index(d.Ecosystem, t.Path, d.Name)
		if err != nil {
			return err
		}
		s.PURL = d.PURL
		out = append(out, s)
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	for _, s := range out {
		fmt.Printf("# %s (%s) — %d symbols\n", s.Dep, s.Ecosystem, s.UsedCount())
		for _, sym := range s.Symbols {
			fmt.Printf("  %s  [%s]  %d site(s)\n", sym.Name, sym.Kind, len(sym.Sites))
			for _, site := range sym.Sites {
				fmt.Printf("    %s:%d  %s\n", site.File, site.Line, truncate(site.Context, 80))
			}
		}
	}
	return nil
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
		if _, err := usage.For(eco); err == nil {
			return hyrum.Dep{Name: name, Ecosystem: eco, Direct: false}, true
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
