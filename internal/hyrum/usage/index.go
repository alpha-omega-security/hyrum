package usage

import (
	"context"
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

// scanWithOptions walks root, extracts imports via outline.Imports, keeps those whose
// module matches one of the dep's provided names, records their bound
// identifiers as receivers, then calls outline.Refs to collect direct member
// accesses on those receivers.
func scanWithOptions(
	ctx context.Context,
	root string,
	sp spec,
	provided []provides.ProvidedName,
	opts IndexOptions,
) (*Surface, error) {
	const key = "dependency"
	surfaces, err := scanManyWithOptions(ctx, root, sp, []scanTarget{{key: key, provided: provided}}, opts)
	return surfaces[key], err
}

type scanTarget struct {
	key      string
	provided []provides.ProvidedName
}

func scanManyWithOptions(
	ctx context.Context,
	root string,
	sp spec,
	targets []scanTarget,
	opts IndexOptions,
) (map[string]*Surface, error) {
	exts := set(sp.exts...)
	collectors := make(map[string]*collector, len(targets))
	for _, target := range targets {
		collectors[target.key] = newCollector()
	}

	err := walkSourceFiles(ctx, root, exts, func(path string) error {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		scope := scopeForPath(rel)
		if !opts.allows(rel, scope) {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return scanFileMany(ctx, collectors, sp, src, rel, scope, targets)
	})
	surfaces := make(map[string]*Surface, len(collectors))
	for key, c := range collectors {
		surfaces[key] = c.surface()
	}
	return surfaces, err
}

func walkSourceFiles(
	ctx context.Context,
	root string,
	exts map[string]bool,
	visit func(string) error,
) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil || d.IsDir() {
			if d != nil && d.IsDir() && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !exts[filepath.Ext(path)] {
			return nil
		}
		if err := visit(path); err != nil {
			return err
		}
		return ctx.Err()
	})
}

type receiverOwner struct {
	collector *collector
	export    string
}

// scanFileMany records symbols for every target from one parse of the file.
// It assigns matching imports and receiver references to each target's
// collector independently.
func scanFileMany(
	ctx context.Context,
	collectors map[string]*collector,
	sp spec,
	src []byte,
	rel string,
	scope Scope,
	targets []scanTarget,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	imports, ok := outline.Imports(src, rel)
	if !ok {
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lines := strings.Split(string(src), "\n")

	receivers, err := seedReceivers(ctx, sp, targets)
	if err != nil {
		return err
	}
	if err := recordMatchingImports(ctx, collectors, sp, imports, rel, scope, lines, targets, receivers); err != nil {
		return err
	}
	owners := collectReceiverOwners(receivers, collectors)
	if len(owners) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	refs, _ := outline.Refs(src, rel, keys(owners))
	if err := ctx.Err(); err != nil {
		return err
	}
	return recordRefs(ctx, owners, refs, rel, scope, lines)
}

// receivers maps each target's local identifiers to export-side names.
// `request as rq` followed by `rq.args` is therefore recorded as
// `request.args` in the collector for the package that provided request.
func seedReceivers(
	ctx context.Context,
	sp spec,
	targets []scanTarget,
) (map[string]map[string]string, error) {
	receivers := make(map[string]map[string]string, len(targets))
	if !sp.seedProvided {
		return receivers, nil
	}
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, n := range target.provided {
			if !isIdentifier(n.Name) {
				continue
			}
			if receivers[target.key] == nil {
				receivers[target.key] = map[string]string{}
			}
			receivers[target.key][n.Name] = n.Name
		}
	}
	return receivers, nil
}

func recordMatchingImports(
	ctx context.Context,
	collectors map[string]*collector,
	sp spec,
	imports []outline.Import,
	rel string,
	scope Scope,
	lines []string,
	targets []scanTarget,
	receivers map[string]map[string]string,
) error {
	for _, imp := range imports {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, target := range targets {
			if !matchesAny(target.provided, imp.Module) {
				continue
			}
			if receivers[target.key] == nil {
				receivers[target.key] = map[string]string{}
			}
			recordImport(
				collectors[target.key],
				sp,
				imp,
				rel,
				scope,
				lineAt(lines, imp.Line),
				receivers[target.key],
			)
		}
	}
	return nil
}

func collectReceiverOwners(
	receivers map[string]map[string]string,
	collectors map[string]*collector,
) map[string][]receiverOwner {
	owners := map[string][]receiverOwner{}
	for key, targetReceivers := range receivers {
		for local, export := range targetReceivers {
			owners[local] = append(owners[local], receiverOwner{
				collector: collectors[key],
				export:    export,
			})
		}
	}
	return owners
}

func recordRefs(
	ctx context.Context,
	owners map[string][]receiverOwner,
	refs []outline.Ref,
	rel string,
	scope Scope,
	lines []string,
) error {
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, owner := range owners[ref.Receiver] {
			owner.collector.add(
				owner.export+"."+ref.Member,
				kindMember,
				rel,
				ref.Line,
				lineAt(lines, ref.Line),
				scope,
			)
		}
	}
	return nil
}

// recordImport records the symbols one matching import statement introduces
// and adds the local identifiers it binds to receivers.
func recordImport(
	c *collector,
	sp spec,
	imp outline.Import,
	rel string,
	scope Scope,
	ctx string,
	receivers map[string]string,
) {
	switch imp.Kind {
	case outline.ImportNamed:
		for _, n := range imp.Names {
			c.add(n.Name, kindNamed, rel, imp.Line, ctx, scope)
			local := n.Alias
			if local == "" {
				local = n.Name
			}
			receivers[local] = n.Name
		}
	case outline.ImportSideEffect, outline.ImportWildcard:
		c.add(imp.Module, string(imp.Kind), rel, imp.Line, ctx, scope)
	default:
		// Module, default, and namespace imports bind one local identifier
		// for the whole module. Record the module path as the consumed
		// symbol and map the local identifier to it so member refs record
		// as module.member regardless of the target's chosen alias. When
		// outline reports no alias, moduleAlias derives one where the
		// ecosystem defines a convention.
		c.add(imp.Module, string(imp.Kind), rel, imp.Line, ctx, scope)
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

func (c *collector) add(name, kind, file string, line int, ctx string, scope Scope) {
	if name == "" {
		return
	}
	s, ok := c.syms[name]
	if !ok {
		s = &Symbol{Name: name, Kind: kind}
		c.syms[name] = s
		c.order = append(c.order, name)
	}
	s.Sites = append(s.Sites, Site{File: file, Line: line, Context: ctx, Scope: scope})
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

func keys[V any](m map[string]V) []string {
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
