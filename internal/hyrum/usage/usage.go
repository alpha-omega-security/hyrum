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
}

// Symbol is one imported/required name from the dependency and every place
// the target references it.
type Symbol struct {
	Name  string `json:"name"`
	Kind  string `json:"kind,omitempty"`
	Sites []Site `json:"sites"`
}

// Surface is the full usage surface of one dependency as seen from one target.
type Surface struct {
	Dep       string   `json:"dep"`       // package name as imported
	PURL      string   `json:"purl"`      // canonical identity
	Ecosystem string   `json:"ecosystem"` // purl type
	Symbols   []Symbol `json:"symbols"`
}

// UsedCount is len(Symbols); ExportedCount comes from a separate lookup on
// the dependency's own source (outline.Pack) and is filled in by the caller.
func (s *Surface) UsedCount() int { return len(s.Symbols) }

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
	p, err := purl.Parse(depPURL)
	if err != nil {
		return nil, fmt.Errorf("usage: parse %q: %w", depPURL, err)
	}
	sp, ok := specs[p.Type]
	if !ok {
		return nil, fmt.Errorf("usage: no indexer for purl type %q (have: %v)", p.Type, Ecosystems())
	}
	res, err := resolver.ResolveSurface(ctx, provides.Package{PURL: depPURL}, provides.SurfaceOptions{})
	if err != nil {
		return nil, fmt.Errorf("usage: resolve %s: %w", depPURL, err)
	}
	s, err := scan(ctx, root, sp, res.Surface.Provides)
	if err != nil {
		return nil, err
	}
	s.PURL = depPURL
	s.Ecosystem = p.Type
	s.Dep = purlName(p)
	return s, nil
}

func purlName(p *purl.PURL) string {
	if p.Namespace != "" {
		return p.Namespace + "/" + p.Name
	}
	return p.Name
}
