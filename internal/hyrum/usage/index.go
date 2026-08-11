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

// spec configures the file walk and receiver seeding for one ecosystem.
// The mapping from a package identity to the source names it provides is
// supplied by git-pkgs/provides; spec covers only what provides cannot,
// which is source-language behaviour rather than package identity.
type spec struct {
	// exts limits the walk to these file extensions. outline dispatches
	// on filename, so this only avoids reading irrelevant files.
	exts []string
	// seedProvided adds each resolved ProvidedName.Name as an
	// unconditional receiver for outline.Refs. Set for languages where a
	// dependency's top-level name can be referenced without an import
	// line: Ruby's Bundler-autoloaded constant, Rust's crate identifier,
	// PHP's PSR-4 root, Elixir's aliased module.
	seedProvided bool
	// moduleAlias derives a receiver from an unaliased module import
	// where the language convention is not the module path itself. Go's
	// `import "github.com/x/y"` binds `y`, not the full path.
	moduleAlias func(module string) string
}

var specs = map[string]spec{
	"npm": {
		exts: []string{".js", ".mjs", ".cjs", ".ts", ".mts", ".cts", ".jsx", ".tsx"},
	},
	"pypi": {
		exts: []string{".py"},
	},
	"golang": {
		exts:        []string{".go"},
		moduleAlias: gopath.Base,
	},
	"gem": {
		exts:         []string{".rb", ".rake", ".gemspec"},
		seedProvided: true,
	},
	"cargo": {
		exts:         []string{".rs"},
		seedProvided: true,
	},
	"composer": {
		exts:         []string{".php"},
		seedProvided: true,
	},
	"hex": {
		exts:         []string{".ex", ".exs"},
		seedProvided: true,
	},
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

// scan walks root, extracts imports via outline.Imports, keeps those whose
// module matches one of the dep's provided names, records their bound
// identifiers as receivers, then calls outline.Refs to collect direct member
// accesses on those receivers.
func scan(root string, sp spec, provided []provides.ProvidedName) (*Surface, error) {
	exts := set(sp.exts...)
	c := newCollector()

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
		scanFile(c, sp, src, rel, provided)
		return nil
	})
	return c.surface(), err
}

// scanFile records symbols from one file into c. It first extracts import
// statements and keeps those whose module string matches a provided name,
// then traces member accesses on the local identifiers those imports bind.
func scanFile(c *collector, sp spec, src []byte, rel string, provided []provides.ProvidedName) {
	imports, ok := outline.Imports(src, rel)
	if !ok {
		return
	}
	lines := strings.Split(string(src), "\n")

	// receivers maps a local identifier to the export-side name that member
	// accesses on it should be recorded under, so `request as rq` followed
	// by `rq.args` is recorded as `request.args`.
	receivers := map[string]string{}
	if sp.seedProvided {
		for _, n := range provided {
			if isIdentifier(n.Name) {
				receivers[n.Name] = n.Name
			}
		}
	}

	for _, imp := range imports {
		if !matchesAny(provided, imp.Module) {
			continue
		}
		recordImport(c, sp, imp, rel, lineAt(lines, imp.Line), receivers)
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
func recordImport(c *collector, sp spec, imp outline.Import, rel, ctx string, receivers map[string]string) {
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
		// outline reports no alias, moduleAlias derives one where the
		// ecosystem defines a convention.
		c.add(imp.Module, string(imp.Kind), rel, imp.Line, ctx)
		alias := ""
		if len(imp.Names) > 0 {
			alias = imp.Names[0].Alias
		}
		if alias == "" && sp.moduleAlias != nil {
			alias = sp.moduleAlias(imp.Module)
		}
		if alias != "" {
			receivers[alias] = imp.Module
		}
	}
}

// collector accumulates symbols across files, keyed by symbol name so
// multiple sites merge. Insertion order is retained for stable output.
type collector struct {
	syms  map[string]*Symbol
	order []string
}

const (
	kindNamed  = "named"
	kindMember = "member"
)

func newCollector() *collector {
	return &collector{syms: map[string]*Symbol{}}
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
	surf := &Surface{Symbols: make([]Symbol, 0, len(c.order))}
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

// isIdentifier reports whether s is a single identifier suitable as a
// receiver name (no path separators, dots, or backslashes). This filters
// ProvidedName entries like a Go module path or an npm subpath from being
// seeded as receivers.
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r == '/' || r == '.' || r == '\\' || r == ':' {
			return false
		}
	}
	return true
}
