package usage

import (
	"path/filepath"
	"strings"
)

// Scope classifies a source site by its path within the target.
type Scope string

const (
	ScopeProduction    Scope = "production"
	ScopeTest          Scope = "test"
	ScopeExample       Scope = "example"
	ScopeDocumentation Scope = "documentation"
)

// IndexOptions limits indexed files by relative path prefix and scope. Empty
// fields include every path and scope.
type IndexOptions struct {
	IncludePaths []string
	ExcludePaths []string
	Scopes       []Scope
}

func (o IndexOptions) allows(rel string, scope Scope) bool {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if len(o.IncludePaths) > 0 && !matchesPathPrefix(o.IncludePaths, rel) {
		return false
	}
	if matchesPathPrefix(o.ExcludePaths, rel) {
		return false
	}
	if len(o.Scopes) == 0 {
		return true
	}
	for _, allowed := range o.Scopes {
		if allowed == scope {
			return true
		}
	}
	return false
}

func matchesPathPrefix(prefixes []string, rel string) bool {
	for _, prefix := range prefixes {
		prefix = filepath.ToSlash(filepath.Clean(prefix))
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			return true
		}
	}
	return false
}

func scopeForPath(rel string) Scope {
	path := strings.ToLower(filepath.ToSlash(rel))
	parts := strings.Split(path, "/")
	dirs := parts[:len(parts)-1]
	name := parts[len(parts)-1]

	if hasPathPart(dirs, "test", "tests", "spec", "specs", "__tests__", "testdata") || isTestFilename(name) {
		return ScopeTest
	}
	if hasPathPart(dirs, "doc", "docs", "documentation") {
		return ScopeDocumentation
	}
	if hasPathPart(dirs, "example", "examples", "sample", "samples", "demo", "demos") {
		return ScopeExample
	}
	return ScopeProduction
}

func hasPathPart(parts []string, names ...string) bool {
	for _, part := range parts {
		for _, name := range names {
			if part == name {
				return true
			}
		}
	}
	return false
}

func isTestFilename(name string) bool {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	return name == "conftest.py" || stem == "test" || stem == "tests" ||
		strings.HasPrefix(stem, "test_") || strings.HasPrefix(stem, "spec_") ||
		strings.HasSuffix(stem, "_test") || strings.HasSuffix(stem, "_spec") ||
		strings.Contains(name, ".test.") || strings.Contains(name, ".spec.")
}
