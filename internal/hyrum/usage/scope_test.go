package usage

import "testing"

func TestScopeForPath(t *testing.T) {
	tests := map[string]Scope{
		"src/app.py":                    ScopeProduction,
		"tests/app.py":                  ScopeTest,
		"pkg/test_client.py":            ScopeTest,
		"pkg/client_test.go":            ScopeTest,
		"pkg/client_test.rb":            ScopeTest,
		"pkg/client_spec.exs":           ScopeTest,
		"pkg/tests.py":                  ScopeTest,
		"pkg/client.spec.ts":            ScopeTest,
		"conftest.py":                   ScopeTest,
		"docs/reference/app.py":         ScopeDocumentation,
		"examples/basic/app.py":         ScopeExample,
		"docs/examples/test_example.py": ScopeTest,
	}
	for path, want := range tests {
		t.Run(path, func(t *testing.T) {
			if got := scopeForPath(path); got != want {
				t.Errorf("scopeForPath(%q) = %q, want %q", path, got, want)
			}
		})
	}
}

func TestIndexRecordsAndFiltersScopes(t *testing.T) {
	root := writeTree(t, map[string]string{
		"src/app.py":        "import flask\n",
		"tests/test_app.py": "import flask\n",
		"conftest.py":       "import flask\n",
		"examples/app.py":   "import flask\n",
		"docs/snippet.py":   "import flask\n",
	})

	all, err := Index(t.Context(), root, "pkg:pypi/flask")
	if err != nil {
		t.Fatal(err)
	}
	wantScopes := map[string]Scope{
		"src/app.py":        ScopeProduction,
		"tests/test_app.py": ScopeTest,
		"conftest.py":       ScopeTest,
		"examples/app.py":   ScopeExample,
		"docs/snippet.py":   ScopeDocumentation,
	}
	for _, symbol := range all.Symbols {
		for _, site := range symbol.Sites {
			want, ok := wantScopes[site.File]
			if !ok {
				t.Errorf("unexpected site %q", site.File)
				continue
			}
			if site.Scope != want {
				t.Errorf("scope for %q = %q, want %q", site.File, site.Scope, want)
			}
			delete(wantScopes, site.File)
		}
	}
	if len(wantScopes) != 0 {
		t.Errorf("missing scoped sites: %v", wantScopes)
	}

	production, err := IndexWithOptions(t.Context(), root, "pkg:pypi/flask", IndexOptions{
		Scopes: []Scope{ScopeProduction},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSiteFiles(t, production, "src/app.py")
}

func TestIndexFiltersPathPrefixes(t *testing.T) {
	root := writeTree(t, map[string]string{
		"src/app.py":              "import flask\n",
		"src/generated/client.py": "import flask\n",
		"other/app.py":            "import flask\n",
	})
	surface, err := IndexWithOptions(t.Context(), root, "pkg:pypi/flask", IndexOptions{
		IncludePaths: []string{"src"},
		ExcludePaths: []string{"src/generated"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSiteFiles(t, surface, "src/app.py")
}

func TestIndexManyFiltersScopes(t *testing.T) {
	root := writeTree(t, map[string]string{
		"src/app.py":        "import flask\n",
		"tests/test_app.py": "import flask\n",
	})
	const dep = "pkg:pypi/flask"
	results, err := IndexManyWithOptions(t.Context(), root, []string{dep}, IndexOptions{
		Scopes: []Scope{ScopeTest},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := results[dep]
	if result.Err != nil || result.Surface == nil {
		t.Fatalf("result = %+v", result)
	}
	assertSiteFiles(t, result.Surface, "tests/test_app.py")
}

func assertSiteFiles(t *testing.T, surface *Surface, want ...string) {
	t.Helper()
	got := map[string]bool{}
	for _, symbol := range surface.Symbols {
		for _, site := range symbol.Sites {
			got[site.File] = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("site files = %v, want %v", got, want)
	}
	for _, file := range want {
		if !got[file] {
			t.Errorf("site files = %v, missing %q", got, file)
		}
	}
}
