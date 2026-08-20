// Package usage extracts how a target repository calls into a specific
// dependency. It is the LLM-free source of truth for which symbols matter,
// feeding both `hyrum surface` and the hyrum-generate skill's context.
//
// Import extraction is delegated to git-pkgs/outline, which parses each
// source file with tree-sitter and returns structured import statements
// and receiver.member references. Mapping a package identity to the module
// or namespace names it provides in source is delegated to
// git-pkgs/provides; a curated Python catalog is chained ahead of the
// naming-convention resolver so packages whose importable name is not a
// mechanical transform of their registry name (PyYAML → yaml) still match.
// Explicit activation strings add sites for dependencies selected through
// driver names, plugin aliases, dynamic imports, or entry points.
//
// Adding an ecosystem is one specs entry: file extensions plus whether the
// language allows referencing a dependency's top-level name without an
// import line.
package usage

import (
	"context"
	"fmt"
	"sort"

	"github.com/git-pkgs/provides"
	"github.com/git-pkgs/provides/curated"
	"github.com/git-pkgs/provides/heuristic"
	"github.com/git-pkgs/purl"
)

// Site is one place in the target where a dependency symbol is referenced.
type Site struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Context string `json:"context"`
	Scope   Scope  `json:"scope"`
}

// Symbol is one dependency entry point and every place the target references
// or activates it.
type Symbol struct {
	Name  string `json:"name"`
	Kind  string `json:"kind,omitempty"`
	Sites []Site `json:"sites"`
}

// Surface is the full usage surface of one dependency as seen from one target.
type Surface struct {
	Dep       string   `json:"dep"`       // package name
	PURL      string   `json:"purl"`      // canonical identity
	Ecosystem string   `json:"ecosystem"` // purl type
	Symbols   []Symbol `json:"symbols"`
}

// UsedCount is len(Symbols); ExportedCount comes from a separate lookup on
// the dependency's own source (outline.Pack) and is filled in by the caller.
func (s *Surface) UsedCount() int { return len(s.Symbols) }

// IndexResult is one dependency's result from IndexMany. Err applies only to
// that dependency; errors that stop a shared scan are returned directly by
// IndexMany.
type IndexResult struct {
	Surface *Surface
	Err     error
}

// resolver is the SurfaceResolver used to map a dependency PURL to the
// source-level names it provides. The curated Python catalog runs first so
// its exact mappings win; the heuristic covers everything else.
var resolver = provides.Chain(curated.Python(), heuristic.Resolver())

// Supported reports whether Index handles the given purl type.
func Supported(purlType string) bool {
	_, ok := specs[purlType]
	return ok
}

// Ecosystems returns the purl types Index handles, sorted.
func Ecosystems() []string {
	out := make([]string, 0, len(specs))
	for k := range specs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Index scans root for source-level references to the dependency identified
// by depPURL and returns the discovered surface.
func Index(ctx context.Context, root, depPURL string) (*Surface, error) {
	return IndexWithOptions(ctx, root, depPURL, IndexOptions{})
}

// IndexWithOptions is Index with path, scope, and activation options.
func IndexWithOptions(
	ctx context.Context,
	root, depPURL string,
	opts IndexOptions,
) (*Surface, error) {
	target, err := resolveTarget(ctx, depPURL)
	if err != nil {
		return nil, err
	}
	s, err := scanWithOptions(
		ctx,
		root,
		target.spec,
		target.provided,
		opts.Activations[target.depPURL],
		opts,
	)
	if err != nil {
		return nil, err
	}
	setSurfaceIdentity(s, target)
	return s, nil
}

type resolvedTarget struct {
	depPURL  string
	parsed   *purl.PURL
	spec     spec
	provided []provides.ProvidedName
}

func resolveTarget(ctx context.Context, depPURL string) (resolvedTarget, error) {
	p, err := purl.Parse(depPURL)
	if err != nil {
		return resolvedTarget{}, fmt.Errorf("usage: parse %q: %w", depPURL, err)
	}
	sp, ok := specs[p.Type]
	if !ok {
		return resolvedTarget{}, fmt.Errorf("usage: no indexer for purl type %q (have: %v)", p.Type, Ecosystems())
	}
	res, err := resolver.ResolveSurface(ctx, provides.Package{PURL: depPURL}, provides.SurfaceOptions{})
	if err != nil {
		return resolvedTarget{}, fmt.Errorf("usage: resolve %s: %w", depPURL, err)
	}
	return resolvedTarget{
		depPURL:  depPURL,
		parsed:   p,
		spec:     sp,
		provided: res.Surface.Provides,
	}, nil
}

// IndexMany scans root once per represented ecosystem and returns a result
// keyed by dependency PURL. Duplicate PURLs are resolved and scanned once.
func IndexMany(ctx context.Context, root string, depPURLs []string) (map[string]IndexResult, error) {
	return IndexManyWithOptions(ctx, root, depPURLs, IndexOptions{})
}

// IndexManyWithOptions is IndexMany with path, scope, and activation options.
func IndexManyWithOptions(
	ctx context.Context,
	root string,
	depPURLs []string,
	opts IndexOptions,
) (map[string]IndexResult, error) {
	results := make(map[string]IndexResult, len(depPURLs))
	groups := map[string][]resolvedTarget{}
	var ecosystems []string
	seen := map[string]bool{}
	for _, depPURL := range depPURLs {
		if seen[depPURL] {
			continue
		}
		seen[depPURL] = true
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		target, resolveErr := resolveTarget(ctx, depPURL)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if resolveErr != nil {
			results[depPURL] = IndexResult{Err: resolveErr}
			continue
		}
		ecosystem := target.parsed.Type
		if len(groups[ecosystem]) == 0 {
			ecosystems = append(ecosystems, ecosystem)
		}
		groups[ecosystem] = append(groups[ecosystem], target)
	}

	for _, ecosystem := range ecosystems {
		targets := groups[ecosystem]
		scanTargets := make([]scanTarget, 0, len(targets))
		for _, target := range targets {
			scanTargets = append(scanTargets, scanTarget{
				key:         target.depPURL,
				provided:    target.provided,
				activations: opts.Activations[target.depPURL],
			})
		}
		surfaces, err := scanManyWithOptions(ctx, root, targets[0].spec, scanTargets, opts)
		if err != nil {
			return nil, err
		}
		for _, target := range targets {
			s := surfaces[target.depPURL]
			setSurfaceIdentity(s, target)
			results[target.depPURL] = IndexResult{Surface: s}
		}
	}
	return results, nil
}

func setSurfaceIdentity(s *Surface, target resolvedTarget) {
	s.PURL = target.depPURL
	s.Ecosystem = target.parsed.Type
	s.Dep = purlName(target.parsed)
}

func purlName(p *purl.PURL) string {
	if p.Namespace != "" {
		return p.Namespace + "/" + p.Name
	}
	return p.Name
}
