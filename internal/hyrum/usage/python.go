package usage

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// pyIndexer covers pypi-ecosystem source. It records `import x` and
// `from x import y` entry points and same-file attribute accesses on the
// resulting bindings; instances and cross-file flow are left to hyrum-usage.
type pyIndexer struct{}

func init() { Register("pypi", pyIndexer{}) }

var (
	// import flask / import flask.json / import flask as f
	rePyImport = regexp.MustCompile(`^\s*import\s+([\w.]+)(?:\s+as\s+(\w+))?`)
	// from flask import Flask, jsonify as j
	rePyFrom = regexp.MustCompile(`^\s*from\s+([\w.]+)\s+import\s+(.+)$`)
)

var pySkipDirs = map[string]bool{
	".git": true, ".venv": true, "venv": true, ".tox": true,
	"__pycache__": true, "node_modules": true,
	"build": true, "dist": true, ".eggs": true,
}

func (pyIndexer) Index(root, dep string) (*Surface, error) {
	surf := &Surface{Dep: dep}
	syms := map[string]*Symbol{}

	add := func(name, kind, file string, line int, ctx string) {
		if name == "" {
			return
		}
		s, ok := syms[name]
		if !ok {
			s = &Symbol{Name: name, Kind: kind}
			syms[name] = s
		}
		s.Sites = append(s.Sites, Site{File: file, Line: line, Context: ctx})
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if pySkipDirs[d.Name()] || strings.HasSuffix(d.Name(), ".egg-info") {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".py" {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		scanPyFile(path, rel, dep, add)
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

func scanPyFile(path, rel, dep string, add func(name, kind, file string, line int, ctx string)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	// Local bindings introduced by importing dep so later attribute accesses
	// can be attributed. binding -> exported name it aliases (or "" for the
	// module itself).
	bindings := map[string]string{}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	// A `from x import (` opens a multi-line group; accumulate until the
	// matching `)` and treat the joined text as one logical import line
	// anchored at the opening line number.
	var pending strings.Builder
	pendingLine := 0
	for sc.Scan() {
		line++
		raw := sc.Text()
		text := raw
		siteLine := line

		if pending.Len() > 0 {
			pending.WriteByte(' ')
			pending.WriteString(strings.TrimSpace(raw))
			if !strings.Contains(raw, ")") {
				continue
			}
			text = pending.String()
			siteLine = pendingLine
			pending.Reset()
		} else if m := rePyFrom.FindStringSubmatch(raw); m != nil && strings.Contains(m[2], "(") && !strings.Contains(m[2], ")") {
			pending.WriteString(strings.TrimSpace(raw))
			pendingLine = line
			continue
		}
		trimmed := strings.TrimSpace(text)

		if m := rePyImport.FindStringSubmatch(text); m != nil && matchesPyDep(m[1], dep) {
			local := m[2]
			if local == "" {
				local = strings.SplitN(m[1], ".", 2)[0]
			}
			bindings[local] = ""
			add(m[1], "module", rel, siteLine, trimmed)
			continue
		}
		if m := rePyFrom.FindStringSubmatch(text); m != nil && matchesPyDep(m[1], dep) {
			names := strings.Trim(m[2], " \t()")
			for _, part := range strings.Split(names, ",") {
				name, local := splitAs(strings.TrimSpace(part))
				if name == "" {
					continue
				}
				bindings[local] = name
				add(name, "named", rel, siteLine, trimmed)
			}
			continue
		}

		for local, exported := range bindings {
			if i := strings.Index(text, local+"."); i >= 0 {
				if i > 0 && isPyIdentByte(text[i-1]) {
					continue // part of a longer identifier
				}
				member := leadingIdent(text[i+len(local)+1:])
				if member == "" {
					continue
				}
				name := local + "." + member
				if exported != "" {
					name = exported + "." + member
				}
				add(name, "member", rel, siteLine, trimmed)
			}
		}
	}
}

// matchesPyDep reports whether an import module path refers to dep. The
// installed distribution name and the top-level module name usually match for
// pypi packages but not always; this checks the common case and the
// underscore/hyphen variant.
func matchesPyDep(mod, dep string) bool {
	top := strings.SplitN(mod, ".", 2)[0]
	if strings.EqualFold(top, dep) {
		return true
	}
	// e.g. dep "PyYAML" imports as "yaml" is NOT handled here; that mapping
	// needs registry metadata. Hyphen/underscore normalisation covers most.
	return strings.EqualFold(top, strings.ReplaceAll(dep, "-", "_"))
}

func isPyIdentByte(c byte) bool {
	return c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}
