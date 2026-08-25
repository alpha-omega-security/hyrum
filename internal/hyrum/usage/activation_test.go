package usage

import "testing"

func TestIndexRecordsConfiguredActivationStrings(t *testing.T) {
	root := writeTree(t, map[string]string{
		"src/settings.py":        "drivers = {\"sqlite\": \"aiosqlite\"}\n",
		"src/comment.py":         "# driver = \"aiosqlite\"\n",
		"src/near.py":            "driver = \"not-aiosqlite\"\n",
		"tests/test_settings.py": "driver = 'aiosqlite'\n",
	})
	const dep = "pkg:pypi/aiosqlite"
	opts := IndexOptions{Activations: map[string][]string{dep: {"aiosqlite"}}}

	all, err := IndexWithOptions(t.Context(), root, dep, opts)
	if err != nil {
		t.Fatal(err)
	}
	assertActivationSites(t, all, map[string]Scope{
		"src/settings.py":        ScopeProduction,
		"tests/test_settings.py": ScopeTest,
	})

	opts.Scopes = []Scope{ScopeProduction}
	production, err := IndexWithOptions(t.Context(), root, dep, opts)
	if err != nil {
		t.Fatal(err)
	}
	assertActivationSites(t, production, map[string]Scope{
		"src/settings.py": ScopeProduction,
	})
}

func TestIndexManyKeepsActivationsWithTheirDependency(t *testing.T) {
	root := writeTree(t, map[string]string{
		"app.py": "plugins = [\"flask-plugin\", 'yaml-plugin']\n",
	})
	const flask = "pkg:pypi/flask"
	const yaml = "pkg:pypi/PyYAML"
	results, err := IndexManyWithOptions(t.Context(), root, []string{flask, yaml}, IndexOptions{
		Activations: map[string][]string{
			flask: {"flask-plugin"},
			yaml:  {"yaml-plugin"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := symbolNames(results[flask].Surface); got["flask-plugin"] != 1 || got["yaml-plugin"] != 0 {
		t.Errorf("flask symbols = %v", got)
	}
	if got := symbolNames(results[yaml].Surface); got["yaml-plugin"] != 1 || got["flask-plugin"] != 0 {
		t.Errorf("PyYAML symbols = %v", got)
	}
}

func TestActivationDoesNotDuplicateImportSite(t *testing.T) {
	root := writeTree(t, map[string]string{
		"app.js": "const ws = require(\"ws\");\n",
	})
	const dep = "pkg:npm/ws"
	surface, err := IndexWithOptions(t.Context(), root, dep, IndexOptions{
		Activations: map[string][]string{dep: {"ws"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, symbol := range surface.Symbols {
		if symbol.Name == "ws" {
			if len(symbol.Sites) != 1 {
				t.Fatalf("ws sites = %v", symbol.Sites)
			}
			if symbol.Kind == kindActivation {
				t.Errorf("import site kind = %q", symbol.Kind)
			}
			return
		}
	}
	t.Fatal("ws symbol missing")
}

func TestQuotedLiterals(t *testing.T) {
	got := set(quotedLiterals(`values = ["double", 'single', `+"`backtick`"+`]`, "app.js")...)
	for _, want := range []string{"double", "single", "backtick"} {
		if !got[want] {
			t.Errorf("quoted literals = %v, missing %q", got, want)
		}
	}
	if got := quotedLiterals(`// configured as "ignored"`, "app.js"); len(got) != 0 {
		t.Errorf("comment literals = %v", got)
	}
	if got := quotedLiterals(`#[link(name = "driver")]`, "lib.rs"); len(got) != 1 || got[0] != "driver" {
		t.Errorf("Rust attribute literals = %v", got)
	}
}

func assertActivationSites(t *testing.T, surface *Surface, want map[string]Scope) {
	t.Helper()
	for _, symbol := range surface.Symbols {
		if symbol.Name != "aiosqlite" {
			continue
		}
		if symbol.Kind != kindActivation {
			t.Errorf("activation kind = %q", symbol.Kind)
		}
		if len(symbol.Sites) != len(want) {
			t.Fatalf("activation sites = %v, want %v", symbol.Sites, want)
		}
		for _, site := range symbol.Sites {
			if want[site.File] != site.Scope {
				t.Errorf("site %q scope = %q, want %q", site.File, site.Scope, want[site.File])
			}
		}
		return
	}
	t.Fatal("aiosqlite activation missing")
}
