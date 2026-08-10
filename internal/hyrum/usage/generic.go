package usage

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/git-pkgs/outline"
)

// outlineIndexer finds import entry points by running outline.Outline on each
// target file and matching the dependency name on the lines outline keeps
// (imports, signatures, comments; bodies stripped). It records entry-point
// files and lines only; member-access tracing is left to the hyrum-usage
// skill. When outline gains structured Imports() (git-pkgs/outline#27) this
// switches from text-scan to iterating that, at which point the js and python
// indexers fold in here too.
type outlineIndexer struct {
	// exts limits the walk to these file extensions.
	exts map[string]bool
	// match reports whether a surviving line refers to dep.
	match func(line, dep string) bool
}

func init() {
	Register("golang", outlineIndexer{
		exts:  set(".go"),
		match: goImportMatch,
	})
	Register("gem", outlineIndexer{
		exts:  set(".rb", ".rake", ".gemspec"),
		match: rubyRequireMatch,
	})
	Register("cargo", outlineIndexer{
		exts:  set(".rs"),
		match: rustUseMatch,
	})
}

var genericSkipDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true,
	"target": true, "build": true, "dist": true,
	".bundle": true, "tmp": true, "log": true,
}

func (ix outlineIndexer) Index(root, dep string) (*Surface, error) {
	surf := &Surface{Dep: dep}
	syms := map[string]*Symbol{}

	add := func(name, file string, line int, ctx string) {
		s, ok := syms[name]
		if !ok {
			s = &Symbol{Name: name, Kind: "import"}
			syms[name] = s
		}
		s.Sites = append(s.Sites, Site{File: file, Line: line, Context: ctx})
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if genericSkipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return fs.SkipDir
			}
			return nil
		}
		if !ix.exts[filepath.Ext(path)] {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		out, ok := outline.Outline(src, path)
		if !ok {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		// outline preserves original line positions by leaving elided ranges
		// as ⋮---- markers; the surviving lines are at their original line
		// numbers only when nothing above them was elided. For entry-point
		// purposes the file is what matters, so record line 0 when the exact
		// position is unknown and let hyrum-usage open the file.
		for i, line := range strings.Split(out, "\n") {
			l := strings.TrimSpace(line)
			if l == "" || strings.HasPrefix(l, "⋮") {
				continue
			}
			if ix.match(l, dep) {
				add(dep, rel, i+1, l)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, s := range syms {
		surf.Symbols = append(surf.Symbols, *s)
	}
	return surf, nil
}

// goImportMatch matches an import path that is the dep module or a package
// under it: `"github.com/x/y"` or `"github.com/x/y/sub"`.
func goImportMatch(line, dep string) bool {
	q := `"` + dep
	i := strings.Index(line, q)
	if i < 0 {
		return false
	}
	after := line[i+len(q):]
	return after == "" || after[0] == '"' || after[0] == '/'
}

// rubyRequireMatch matches `require 'gem'`, `require "gem"`, or
// `require 'gem/sub'`.
func rubyRequireMatch(line, dep string) bool {
	if !strings.HasPrefix(line, "require") {
		return false
	}
	for _, q := range []string{`'`, `"`} {
		needle := q + dep
		if i := strings.Index(line, needle); i >= 0 {
			after := line[i+len(needle):]
			if after == "" || after[0] == q[0] || after[0] == '/' {
				return true
			}
		}
	}
	return false
}

// rustUseMatch matches `use crate_name` or `use crate_name::path`.
// Cargo.toml package names use hyphens; crate identifiers use underscores.
func rustUseMatch(line, dep string) bool {
	crate := strings.ReplaceAll(dep, "-", "_")
	for _, pre := range []string{"use ", "extern crate "} {
		if strings.HasPrefix(line, pre) {
			rest := strings.TrimPrefix(line, pre)
			if strings.HasPrefix(rest, crate) {
				after := rest[len(crate):]
				if after == "" || after[0] == ':' || after[0] == ';' || after[0] == ' ' {
					return true
				}
			}
		}
	}
	return false
}

func set(exts ...string) map[string]bool {
	m := make(map[string]bool, len(exts))
	for _, e := range exts {
		m[e] = true
	}
	return m
}
