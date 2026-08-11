package usage

import (
	"io/fs"
	"os"
	gopath "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/git-pkgs/outline"
	"github.com/git-pkgs/provides"
)

// spec configures the tree-sitter indexer for one ecosystem. exts limits the
// file walk; names returns the source-visible module names that dep provides
// (git-pkgs/provides ProvidedName so its Matches method handles subpath and
// submodule boundaries); seed returns receiver identifiers to trace even when
// no import line is present, which covers Ruby's Bundler-autoloaded constants
// and Go's implicit package name for an unaliased import.
type spec struct {
	exts  []string
	names func(dep string) []provides.ProvidedName
	seed  func(dep, module string) string
}

// specs holds the per-ecosystem heuristic name mappings. The mappings encode
// each ecosystem's dep-name → source-name convention; git-pkgs/provides is
// the authority for exact mappings and these heuristics defer to it once a
// resolver exists for the ecosystem.
var specs = map[string]spec{
	"npm": {
		exts: []string{".js", ".mjs", ".cjs", ".ts", ".mts", ".cts", ".jsx", ".tsx"},
		names: func(dep string) []provides.ProvidedName {
			return []provides.ProvidedName{
				{Language: "javascript", Name: dep, Match: provides.MatchPrefix, Separator: "/"},
			}
		},
	},
	"pypi": {
		exts: []string{".py"},
		names: func(dep string) []provides.ProvidedName {
			// PyPI distribution names are case-insensitive and treat -/_/.
			// as equivalent (PEP 503); module names are case-sensitive and
			// conventionally lowercase with underscores. Distributions
			// whose module name differs beyond that convention (PyYAML →
			// yaml, Pillow → PIL) need curated data.
			return []provides.ProvidedName{
				{Language: "python", Name: strings.ToLower(underscore(dep)), Match: provides.MatchPrefix, Separator: "."},
			}
		},
	},
	"golang": {
		exts: []string{".go"},
		names: func(dep string) []provides.ProvidedName {
			return []provides.ProvidedName{
				{Language: "go", Name: dep, Match: provides.MatchPrefix, Separator: "/"},
			}
		},
		// An unaliased Go import binds the last path segment as the package
		// identifier. outline reports no Name for that form, so seed it here.
		seed: func(_, module string) string { return gopath.Base(module) },
	},
	"gem": {
		exts: []string{".rb", ".rake", ".gemspec"},
		names: func(dep string) []provides.ProvidedName {
			return []provides.ProvidedName{
				{Language: "ruby", Name: dep, Match: provides.MatchPrefix, Separator: "/"},
			}
		},
		// Rails apps typically have no require line because Bundler.require
		// autoloads every Gemfile entry, so the camelised constant is the
		// only source-level signal.
		seed: func(dep, _ string) string { return camelize(dep) },
	},
	"cargo": {
		exts: []string{".rs"},
		names: func(dep string) []provides.ProvidedName {
			return []provides.ProvidedName{
				{Language: "rust", Name: underscore(dep), Match: provides.MatchPrefix, Separator: "::"},
			}
		},
		seed: func(dep, _ string) string { return underscore(dep) },
	},
	"composer": {
		exts: []string{".php"},
		names: func(dep string) []provides.ProvidedName {
			// Composer package names are vendor/package; PSR-4 namespaces
			// conventionally start with the titlecased vendor. PHP
			// namespace resolution is case-insensitive at the language
			// level, so the guess matches after case folding; the exact
			// autoload map is a git-pkgs/provides concern.
			return []provides.ProvidedName{{
				Language: "php", Name: phpVendor(dep),
				Match: provides.MatchPrefix, Separator: `\`, CaseInsensitive: true,
			}}
		},
		seed: func(dep, _ string) string { return phpVendor(dep) },
	},
	"hex": {
		exts: []string{".ex", ".exs"},
		names: func(dep string) []provides.ProvidedName {
			return []provides.ProvidedName{
				{Language: "elixir", Name: camelize(dep), Match: provides.MatchPrefix, Separator: "."},
			}
		},
		seed: func(dep, _ string) string { return camelize(dep) },
	},
}

func init() {
	for eco, s := range specs {
		Register(eco, treeIndexer{spec: s})
	}
}

// skipDirs are directories whose contents are never source authored by the
// target itself: version control, vendored dependencies, virtualenvs,
// build output, and caches.
var skipDirs = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
	"target":       true,
	"build":        true,
	"dist":         true,
	"coverage":     true,
	".venv":        true,
	"venv":         true,
	".tox":         true,
	"__pycache__":  true,
	".eggs":        true,
	".bundle":      true,
	"tmp":          true,
	"log":          true,
}

// treeIndexer walks the target's source files, extracts imports via
// outline.Imports, keeps those whose module matches one of the dep's
// provided names, records their bound identifiers as receivers, then calls
// outline.Refs to collect direct member accesses on those receivers.
type treeIndexer struct {
	spec spec
}

func (ix treeIndexer) Index(root, dep string) (*Surface, error) {
	provided := ix.spec.names(dep)
	exts := set(ix.spec.exts...)
	c := newCollector(dep)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !exts[filepath.Ext(path)] {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		ix.scanFile(c, src, rel, provided)
		return nil
	})
	return c.surface(), err
}

// scanFile records symbols from one file into c. It first extracts import
// statements and keeps those whose module string matches a provided name,
// then traces member accesses on the local identifiers those imports bind.
func (ix treeIndexer) scanFile(c *collector, src []byte, rel string, provided []provides.ProvidedName) {
	imports, ok := outline.Imports(src, rel)
	if !ok {
		return
	}
	lines := strings.Split(string(src), "\n")

	// receivers maps a local identifier to the export-side name that member
	// accesses on it should be recorded under, so `request as rq` followed
	// by `rq.args` is recorded as `request.args`.
	receivers := map[string]string{}
	if ix.spec.seed != nil {
		if r := ix.spec.seed(c.dep, ""); r != "" {
			receivers[r] = r
		}
	}

	for _, imp := range imports {
		if !matchesAny(provided, imp.Module) {
			continue
		}
		ix.recordImport(c, imp, rel, lineAt(lines, imp.Line), receivers)
	}
	if len(receivers) == 0 {
		return
	}

	refs, _ := outline.Refs(src, rel, keys(receivers))
	for _, ref := range refs {
		base := receivers[ref.Receiver]
		c.add(base+"."+ref.Member, kindMember, rel, ref.Line, lineAt(lines, ref.Line))
	}
}

// recordImport records the symbols one matching import statement introduces
// and adds the local identifiers it binds to receivers.
func (ix treeIndexer) recordImport(c *collector, imp outline.Import, rel, ctx string, receivers map[string]string) {
	switch imp.Kind {
	case outline.ImportNamed:
		for _, n := range imp.Names {
			c.add(n.Name, kindNamed, rel, imp.Line, ctx)
			local := n.Alias
			if local == "" {
				local = n.Name
			}
			receivers[local] = n.Name
		}
	case outline.ImportSideEffect, outline.ImportWildcard:
		c.add(imp.Module, string(imp.Kind), rel, imp.Line, ctx)
	default:
		// Module, default, and namespace imports bind one local identifier
		// for the whole module. Record the module path as the consumed
		// symbol and map the local identifier to it so member refs record
		// as module.member regardless of the target's chosen alias. When
		// outline reports no alias, seed derives one from the module path
		// where the ecosystem defines a convention.
		c.add(imp.Module, string(imp.Kind), rel, imp.Line, ctx)
		alias := ""
		if len(imp.Names) > 0 {
			alias = imp.Names[0].Alias
		}
		if alias == "" && ix.spec.seed != nil {
			alias = ix.spec.seed(c.dep, imp.Module)
		}
		if alias != "" {
			receivers[alias] = imp.Module
		}
	}
}

// collector accumulates symbols across files, keyed by symbol name so
// multiple sites merge. Insertion order is retained for stable output.
type collector struct {
	dep   string
	syms  map[string]*Symbol
	order []string
}

const (
	kindNamed  = "named"
	kindMember = "member"
)

func newCollector(dep string) *collector {
	return &collector{dep: dep, syms: map[string]*Symbol{}}
}

func (c *collector) add(name, kind, file string, line int, ctx string) {
	if name == "" {
		return
	}
	s, ok := c.syms[name]
	if !ok {
		s = &Symbol{Name: name, Kind: kind}
		c.syms[name] = s
		c.order = append(c.order, name)
	}
	s.Sites = append(s.Sites, Site{File: file, Line: line, Context: ctx})
}

func (c *collector) surface() *Surface {
	surf := &Surface{Dep: c.dep, Symbols: make([]Symbol, 0, len(c.order))}
	for _, name := range c.order {
		surf.Symbols = append(surf.Symbols, *c.syms[name])
	}
	return surf
}

func matchesAny(names []provides.ProvidedName, module string) bool {
	for _, n := range names {
		if n.Matches(module) {
			return true
		}
	}
	return false
}

func skipDir(name string) bool {
	return skipDirs[name] || (strings.HasPrefix(name, ".") && name != ".") ||
		strings.HasSuffix(name, ".egg-info")
}

func lineAt(lines []string, n int) string {
	if n < 1 || n > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[n-1])
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

// underscore replaces hyphens with underscores, which is how PyPI
// distribution names and Cargo package names map onto importable module and
// crate identifiers by default.
func underscore(s string) string { return strings.ReplaceAll(s, "-", "_") }

// camelize turns a hyphen/underscore-separated package name into its
// conventional constant form: octokit → Octokit, active_support →
// ActiveSupport, faraday-retry → FaradayRetry.
func camelize(s string) string {
	var b strings.Builder
	up := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' || c == '-' {
			up = true
			continue
		}
		if up && 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		up = false
		b.WriteByte(c)
	}
	return b.String()
}

// phpVendor derives the top-level PSR-4 namespace segment from a composer
// package name. guzzlehttp/guzzle → Guzzlehttp. The exact autoload map lives
// in the package's composer.json and is a git-pkgs/provides concern.
func phpVendor(dep string) string {
	vendor := dep
	if i := strings.IndexByte(dep, '/'); i > 0 {
		vendor = dep[:i]
	}
	return camelize(vendor)
}
