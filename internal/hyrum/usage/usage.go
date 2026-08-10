// Package usage extracts how a target repository calls into a specific
// dependency. It is the "usage-index" step in the plan: the LLM-free source
// of truth for which symbols matter, feeding both `hyrum surface` and the
// hyrum-generate skill's context.
//
// Per-ecosystem extraction is registered against ecosystem strings that match
// the ones git-pkgs/manifests emits (npm, pypi, go, cargo, ...), so adding a
// language is `Register("cargo", rustIndexer{})` and nothing else changes.
package usage

import (
	"fmt"
	"sort"
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
	Kind  string `json:"kind,omitempty"` // default, named, namespace, cjs
	Sites []Site `json:"sites"`
}

// Surface is the full usage surface of one dependency as seen from one target.
type Surface struct {
	Dep       string   `json:"dep"`       // package name as imported
	PURL      string   `json:"purl"`      // canonical identity
	Ecosystem string   `json:"ecosystem"` // manifests ecosystem string
	Symbols   []Symbol `json:"symbols"`
}

// UsedCount is len(Symbols); ExportedCount comes from a separate lookup on
// the dependency's own source (outline.Pack) and is filled in by the caller.
func (s *Surface) UsedCount() int { return len(s.Symbols) }

// Indexer extracts a Surface for one ecosystem. Implementations should be
// pure functions of the filesystem under root.
type Indexer interface {
	// Index scans root for references to the dependency named dep and
	// returns the discovered surface. dep is the import name (e.g. "ws",
	// "flask"), not the purl.
	Index(root, dep string) (*Surface, error)
}

var indexers = map[string]Indexer{}

// Register makes an Indexer available under the given ecosystem key. Keys
// must match git-pkgs/manifests ecosystem strings.
func Register(ecosystem string, ix Indexer) { indexers[ecosystem] = ix }

// For returns the registered indexer for ecosystem, or an error listing what
// is available.
func For(ecosystem string) (Indexer, error) {
	if ix, ok := indexers[ecosystem]; ok {
		return ix, nil
	}
	keys := make([]string, 0, len(indexers))
	for k := range indexers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return nil, fmt.Errorf("no usage indexer for ecosystem %q (have: %v)", ecosystem, keys)
}

// Index is a convenience wrapper that looks up the indexer for ecosystem and
// runs it.
func Index(ecosystem, root, dep string) (*Surface, error) {
	ix, err := For(ecosystem)
	if err != nil {
		return nil, err
	}
	s, err := ix.Index(root, dep)
	if err != nil {
		return nil, err
	}
	s.Ecosystem = ecosystem
	s.Dep = dep
	return s, nil
}
