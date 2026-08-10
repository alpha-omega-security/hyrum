package usage

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// jsIndexer covers npm-ecosystem source (JavaScript and TypeScript). It finds
// import/require entry points and same-file member accesses on the resulting
// bindings; it does not follow values across files or into instances.
type jsIndexer struct{}

func init() { Register("npm", jsIndexer{}) }

var (
	// require('dep') / require("dep/sub")
	reRequire = regexp.MustCompile(`\brequire\(\s*['"]([^'"]+)['"]\s*\)`)
	// import ... from 'dep'
	reImportFrom = regexp.MustCompile(`\bimport\s+(.+?)\s+from\s+['"]([^'"]+)['"]`)
	// bare: import 'dep'
	reImportBare = regexp.MustCompile(`\bimport\s+['"]([^'"]+)['"]`)
)

var jsExts = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true,
	".ts": true, ".mts": true, ".cts": true,
	".jsx": true, ".tsx": true,
}

var jsSkipDirs = map[string]bool{
	"node_modules": true, ".git": true,
	"dist": true, "build": true, "coverage": true,
}

func (jsIndexer) Index(root, dep string) (*Surface, error) {
	surf := &Surface{Dep: dep}
	syms := map[string]*Symbol{}

	add := func(name, kind, file string, line int, ctx string) {
		if name == "" {
			name = dep
		}
		s, ok := syms[name]
		if !ok {
			s = &Symbol{Name: name, Kind: kind}
			syms[name] = s
			surf.Symbols = append(surf.Symbols, Symbol{}) // placeholder for stable order
		}
		s.Sites = append(s.Sites, Site{File: file, Line: line, Context: ctx})
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if jsSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !jsExts[filepath.Ext(path)] {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		scanJSFile(path, rel, dep, add)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Rebuild ordered slice from the map, preserving first-seen order via
	// the placeholder trick above being dropped and replaced.
	ordered := make([]Symbol, 0, len(syms))
	seen := map[string]bool{}
	for _, s := range surf.Symbols {
		_ = s // order marker only
	}
	for name, s := range syms {
		if seen[name] {
			continue
		}
		seen[name] = true
		ordered = append(ordered, *s)
	}
	surf.Symbols = ordered
	return surf, nil
}

func scanJSFile(path, rel, dep string, add func(name, kind, file string, line int, ctx string)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	// Track local bindings introduced by importing dep so later member
	// accesses can be attributed. binding -> kind.
	bindings := map[string]string{}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()

		if m := reRequire.FindStringSubmatchIndex(text); m != nil {
			spec := text[m[2]:m[3]]
			if matchesDep(spec, dep) {
				bs := parseRequireBindings(text)
				// If the require result is immediately dereferenced
				// (const X = require('d').Y), record Y as the consumed
				// export rather than the local name X.
				chained := ""
				if len(text) > m[1] && text[m[1]] == '.' {
					chained = leadingIdent(text[m[1]+1:])
				}
				switch {
				case chained != "" && len(bs) > 0:
					for _, b := range bs {
						bindings[b.name] = "named"
						add(chained, "named", rel, line, strings.TrimSpace(text))
					}
				case chained != "":
					add(chained, "named", rel, line, strings.TrimSpace(text))
				case len(bs) > 0:
					for _, b := range bs {
						bindings[b.name] = b.kind
						add(b.symbol, b.kind, rel, line, strings.TrimSpace(text))
					}
				default:
					add(dep, "cjs", rel, line, strings.TrimSpace(text))
				}
				continue
			}
		}
		if m := reImportFrom.FindStringSubmatch(text); m != nil && matchesDep(m[2], dep) {
			for _, b := range parseImportBindings(m[1]) {
				bindings[b.name] = b.kind
				add(b.symbol, b.kind, rel, line, strings.TrimSpace(text))
			}
			continue
		}
		if m := reImportBare.FindStringSubmatch(text); m != nil && matchesDep(m[1], dep) {
			add(dep, "side-effect", rel, line, strings.TrimSpace(text))
			continue
		}

		// Attribute member accesses on known bindings: ws.Server, WebSocket.OPEN
		for local := range bindings {
			if i := strings.Index(text, local+"."); i >= 0 {
				rest := text[i+len(local)+1:]
				member := leadingIdent(rest)
				if member != "" {
					add(local+"."+member, "member", rel, line, strings.TrimSpace(text))
				}
			}
		}
	}
}

// matchesDep reports whether an import specifier refers to dep or a subpath
// of it (e.g. "ws", "ws/lib/websocket").
func matchesDep(spec, dep string) bool {
	return spec == dep || strings.HasPrefix(spec, dep+"/")
}

type binding struct {
	name   string // local identifier in the target file
	symbol string // symbol name to record on the Surface
	kind   string
}

// parseImportBindings handles the LHS of `import LHS from 'dep'`.
//
//	Foo                     -> default
//	* as Foo                -> namespace
//	{ A, B as C }           -> named A (local A), named B (local C)
//	Foo, { A }              -> default + named
func parseImportBindings(lhs string) []binding {
	lhs = strings.TrimSpace(lhs)
	var out []binding
	// default before optional named group
	if i := strings.Index(lhs, "{"); i >= 0 {
		head := strings.TrimSpace(strings.TrimSuffix(lhs[:i], ","))
		if head != "" {
			out = append(out, parseImportBindings(head)...)
		}
		inside := lhs[i+1:]
		if j := strings.Index(inside, "}"); j >= 0 {
			inside = inside[:j]
		}
		for _, part := range strings.Split(inside, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, local := splitAs(part)
			out = append(out, binding{name: local, symbol: name, kind: "named"})
		}
		return out
	}
	if strings.HasPrefix(lhs, "* as ") {
		local := strings.TrimSpace(strings.TrimPrefix(lhs, "* as "))
		return []binding{{name: local, symbol: local, kind: "namespace"}}
	}
	if lhs != "" {
		return []binding{{name: lhs, symbol: lhs, kind: "default"}}
	}
	return nil
}

// parseRequireBindings handles `const X = require(...)` and
// `const { A, B: C } = require(...)`.
func parseRequireBindings(line string) []binding {
	// Trim to the part between const/let/var and =
	eq := strings.Index(line, "=")
	if eq < 0 {
		return nil
	}
	lhs := line[:eq]
	for _, kw := range []string{"const ", "let ", "var "} {
		if i := strings.Index(lhs, kw); i >= 0 {
			lhs = lhs[i+len(kw):]
		}
	}
	lhs = strings.TrimSpace(lhs)
	if strings.HasPrefix(lhs, "{") {
		inside := strings.TrimPrefix(lhs, "{")
		if j := strings.Index(inside, "}"); j >= 0 {
			inside = inside[:j]
		}
		var out []binding
		for _, part := range strings.Split(inside, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, local := splitColon(part)
			out = append(out, binding{name: local, symbol: name, kind: "named"})
		}
		return out
	}
	if lhs != "" && isIdent(lhs) {
		return []binding{{name: lhs, symbol: lhs, kind: "cjs"}}
	}
	return nil
}

func splitAs(s string) (name, local string) {
	if i := strings.Index(s, " as "); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+4:])
	}
	return s, s
}

func splitColon(s string) (name, local string) {
	if i := strings.Index(s, ":"); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return s, s
}

func leadingIdent(s string) string {
	for i, r := range s {
		if r == '_' || r == '$' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || (i > 0 && '0' <= r && r <= '9') {
			continue
		}
		return s[:i]
	}
	return s
}

func isIdent(s string) bool {
	return s != "" && leadingIdent(s) == s
}
