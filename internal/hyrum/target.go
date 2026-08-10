// Package hyrum wires git-pkgs libraries into the Hyrum's-test generation
// pipeline. Nothing here is ecosystem-specific; per-ecosystem behaviour lives
// behind the usage.Indexer registry and the managers package.
package hyrum

import (
	"fmt"
	"path/filepath"

	"github.com/git-pkgs/brief"
	"github.com/git-pkgs/brief/detect"
	"github.com/git-pkgs/brief/kb"
	"github.com/git-pkgs/purl"
)

// Target is a repository to generate Hyrum's tests for, plus everything
// discovered about it that later pipeline stages need.
type Target struct {
	Path   string
	Report *brief.Report
	Deps   []Dep
}

// Dep is one direct dependency of the target. PURL is the join key across
// registries, vulns, and the generated test layout. Ecosystem is derived from
// the PURL type so it always agrees with what registries/managers expect.
type Dep struct {
	Name      string
	Version   string
	PURL      string
	Ecosystem string
	Scope     string
	Direct    bool
}

// Analyze runs brief over path and returns a populated Target. brief already
// discovers manifests, parses them via git-pkgs/manifests, merges manifest and
// lockfile entries, and marks direct vs transitive; we take its list as-is and
// only add the ecosystem string parsed from each PURL.
func Analyze(path string) (*Target, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	kbase, err := kb.Load(brief.KnowledgeFS)
	if err != nil {
		return nil, fmt.Errorf("brief kb: %w", err)
	}
	rep, err := detect.New(kbase, abs).Run()
	if err != nil {
		return nil, fmt.Errorf("brief: %w", err)
	}

	deps := make([]Dep, 0, len(rep.Dependencies))
	for _, d := range rep.Dependencies {
		deps = append(deps, Dep{
			Name:      d.Name,
			Version:   d.Version,
			PURL:      d.PURL,
			Ecosystem: ecosystemOf(d.PURL),
			Scope:     d.Scope,
			Direct:    d.Direct,
		})
	}

	return &Target{Path: abs, Report: rep, Deps: deps}, nil
}

func ecosystemOf(p string) string {
	u, err := purl.Parse(p)
	if err != nil {
		return ""
	}
	return u.Type
}

// Ecosystems returns the distinct ecosystems present in t.Deps, in first-seen
// order. Callers use this to pick usage indexers and package managers without
// assuming a single-language repo.
func (t *Target) Ecosystems() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range t.Deps {
		if d.Ecosystem == "" || seen[d.Ecosystem] {
			continue
		}
		seen[d.Ecosystem] = true
		out = append(out, d.Ecosystem)
	}
	return out
}
