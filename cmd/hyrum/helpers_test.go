package main

import (
	"testing"

	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/git-pkgs/brief"
	"github.com/git-pkgs/outline"
)

func TestOutRoot(t *testing.T) {
	if got := outRoot("/repo", "tests/hyrum"); got != "/repo/tests/hyrum" {
		t.Errorf("relative: %q", got)
	}
	if got := outRoot("/repo", "/abs/out"); got != "/abs/out" {
		t.Errorf("absolute: %q", got)
	}
}

func TestValidateRelativePath(t *testing.T) {
	for _, value := range []string{
		"ws",
		"@scope/pkg",
		"github.com/alpha-omega-security/harness",
		"vendor/package",
	} {
		if err := validateRelativePath("dependency name", value); err != nil {
			t.Errorf("validateRelativePath(%q): %v", value, err)
		}
	}

	for _, value := range []string{
		"",
		".",
		"..",
		"../escape",
		"../../escape",
		"pkg/../../escape",
		"pkg/../other",
		"/absolute",
		"pkg/",
	} {
		if err := validateRelativePath("dependency name", value); err == nil {
			t.Errorf("validateRelativePath(%q) succeeded", value)
		}
	}
}

func TestRemoteBasename(t *testing.T) {
	cases := map[string]string{
		"https://github.com/octobox/octobox.git": "octobox",
		"https://github.com/octobox/octobox":     "octobox",
		"git@github.com:octobox/octobox.git":     "octobox",
		"dokku@51.159.56.10:octobox":             "dokku@51.159.56.10:octobox",
	}
	for in, want := range cases {
		if got := remoteBasename(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestTargetNamePrefersOrigin(t *testing.T) {
	tgt := &hyrum.Target{
		Path: "/tmp/x",
		Report: &brief.Report{
			Git: &brief.GitInfo{Remotes: map[string]string{
				"dokku":  "dokku@host:app",
				"origin": "https://github.com/owner/repo.git",
			}},
		},
	}
	if got := targetName(tgt); got != "repo" {
		t.Errorf("got %q, want repo", got)
	}
	// No origin: prefer https over ssh/deploy.
	tgt.Report.Git.Remotes = map[string]string{
		"dokku": "dokku@host:app",
		"fork":  "https://github.com/me/repo.git",
	}
	if got := targetName(tgt); got != "repo" {
		t.Errorf("no-origin: got %q, want repo", got)
	}
	// No git info: path basename.
	tgt.Report.Git = nil
	if got := targetName(tgt); got != "x" {
		t.Errorf("no-git: got %q", got)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"https://github.com/a/b":  "github_com_a_b",
		"git@github.com:a/b.git":  "github_com_a_b_git",
		"http://localhost:3000/x": "localhost_3000_x",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestSplitDependentSpec(t *testing.T) {
	cases := []struct{ in, url, ref string }{
		{"https://github.com/a/b", "https://github.com/a/b", ""},
		{"https://github.com/a/b@v1.2.3", "https://github.com/a/b", "v1.2.3"},
		{"https://token@github.com/a/b", "https://token@github.com/a/b", ""},
		{"https://token@github.com/a/b@main", "https://token@github.com/a/b", "main"},
		{"git@github.com:a/b.git", "git@github.com:a/b.git", ""},
		{"git@github.com:a/b.git@main", "git@github.com:a/b.git", "main"},
	}
	for _, c := range cases {
		u, r := splitDependentSpec(c.in)
		if u != c.url || r != c.ref {
			t.Errorf("%q: got (%q,%q) want (%q,%q)", c.in, u, r, c.url, c.ref)
		}
	}
}

func TestVersionOr(t *testing.T) {
	if versionOr("1.0", "2.0") != "1.0" {
		t.Error("prefer explicit")
	}
	if versionOr("", "2.0") != "2.0" {
		t.Error("fallback")
	}
	if versionOr("", "") != "installed" {
		t.Error("default")
	}
}

func TestIndent(t *testing.T) {
	if got := indent("a\nb\n", "  "); got != "  a\n  b" {
		t.Errorf("got %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if truncate("abcdef", 4) != "abc…" {
		t.Errorf("got %q", truncate("abcdef", 4))
	}
	if truncate("ab", 4) != "ab" {
		t.Errorf("short: %q", truncate("ab", 4))
	}
}

func TestConstraintVersion(t *testing.T) {
	cases := []struct{ in, eco, want string }{
		{"^1.2.3", "npm", "1.2.3"},
		{"~7.4.2", "npm", "7.4.2"},
		{"8.17.1", "npm", "8.17.1"},
		{"*", "npm", ""},
		{"<3", "npm", ""},
		{"", "npm", ""},
		{"~> 4.0", "gem", "4.0"},
		{"~=2.3", "pypi", "2.3"},
		{">=2.3,!=2.3", "pypi", ""},
		{"v10.28.0", "golang", "v10.28.0"},
	}
	for _, c := range cases {
		if got := constraintVersion(c.in, c.eco); got != c.want {
			t.Errorf("(%q, %s): got %q want %q", c.in, c.eco, got, c.want)
		}
	}
}

func TestCountExported(t *testing.T) {
	r := &outline.Result{Files: []outline.File{
		{Path: "lib/a.js", Symbols: []outline.Symbol{
			{Name: "Public", Exported: true},
			{Name: "internal", Exported: false},
		}},
		{Path: "test/a_test.js", Symbols: []outline.Symbol{
			{Name: "TestX", Exported: true},
		}},
		{Path: "b_test.go", Symbols: []outline.Symbol{
			{Name: "TestY", Exported: true},
		}},
	}}
	if got := countExported(r); got != 1 {
		t.Errorf("got %d, want 1 (test files excluded)", got)
	}
}
