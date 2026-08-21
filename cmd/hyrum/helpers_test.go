package main

import (
	"strings"
	"testing"

	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/git-pkgs/brief"
	"github.com/git-pkgs/outline"
	"github.com/git-pkgs/registries"
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

func TestTargetNamePrefersAnalyzedPackageIdentity(t *testing.T) {
	tgt := &hyrum.Target{
		Path: "/tmp/airflow-core",
		Name: "apache-airflow-core",
		Report: &brief.Report{Git: &brief.GitInfo{Remotes: map[string]string{
			"origin": "https://github.com/apache/airflow.git",
		}}},
	}
	if got := targetName(tgt); got != "apache-airflow-core" {
		t.Fatalf("got %q, want apache-airflow-core", got)
	}
}

func TestValidateTargetName(t *testing.T) {
	for _, value := range []string{"airflow", "apache-airflow-core", "project.name_v2"} {
		if err := validateTargetName(value); err != nil {
			t.Errorf("validateTargetName(%q): %v", value, err)
		}
	}
	for _, value := range []string{"", ".", "..", "../escape", "scope/package", `scope\package`} {
		if err := validateTargetName(value); err == nil {
			t.Errorf("validateTargetName(%q) succeeded", value)
		}
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

func TestResolveBaseline(t *testing.T) {
	versions := []registries.Version{
		{Number: "9.0.0"},
		{Number: "8.10.1"},
		{Number: "8.9.0"},
		{Number: "8.10.0"},
		{Number: "not-a-version"},
	}
	baseline, err := resolveBaseline(">=8.10,<10", hyrum.EcoPyPI, versions)
	if err != nil {
		t.Fatal(err)
	}
	if baseline != "8.10.0" {
		t.Fatalf("baseline = %q, want 8.10.0", baseline)
	}

	statusVersions := []registries.Version{
		{Number: "8.10.0", Status: registries.StatusYanked},
		{Number: "8.10.1", Status: registries.StatusRetracted},
		{Number: "8.10.2", Status: registries.StatusDeprecated},
		{Number: "8.11.0"},
	}
	baseline, err = resolveBaseline(">=8.10,<10", hyrum.EcoPyPI, statusVersions)
	if err != nil {
		t.Fatal(err)
	}
	if baseline != "8.10.2" {
		t.Fatalf("status-filtered baseline = %q, want 8.10.2", baseline)
	}

	baseline, err = resolveBaseline("==8.10.0", hyrum.EcoPyPI, statusVersions)
	if err != nil {
		t.Fatal(err)
	}
	if baseline != "8.10.0" {
		t.Fatalf("exact yanked baseline = %q, want 8.10.0", baseline)
	}
}

func TestResolveBaselineOtherRangeShapes(t *testing.T) {
	versions := []registries.Version{{Number: "1.0.0"}, {Number: "2.3"}, {Number: "2.3.1"}}
	for _, test := range []struct {
		constraint string
		ecosystem  string
		want       string
	}{
		{constraint: "*", ecosystem: hyrum.EcoNPM, want: "1.0.0"},
		{constraint: "<3", ecosystem: hyrum.EcoPyPI, want: "1.0.0"},
		{constraint: ">=2.3,!=2.3", ecosystem: hyrum.EcoPyPI, want: "2.3.1"},
	} {
		baseline, err := resolveBaseline(test.constraint, test.ecosystem, versions)
		if err != nil {
			t.Errorf("resolveBaseline(%q): %v", test.constraint, err)
		} else if baseline != test.want {
			t.Errorf("resolveBaseline(%q) = %q, want %q", test.constraint, baseline, test.want)
		}
	}
	for _, constraint := range []string{"", ">="} {
		baseline, err := resolveBaseline(constraint, hyrum.EcoPyPI, versions)
		if err == nil || baseline != "" {
			t.Errorf("resolveBaseline(%q) = (%q, %v), want error", constraint, baseline, err)
		}
	}
	if baseline, err := resolveBaseline(">=8.10,<10", hyrum.EcoPyPI, versions); err == nil || baseline != "" || !strings.Contains(err.Error(), "no usable registry release") {
		t.Fatalf("no-match result = (%q, %v)", baseline, err)
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
